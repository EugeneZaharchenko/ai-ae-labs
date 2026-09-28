# Testing ADK Go Workflows Offline

`go test ./...` in this repository must pass with no key and no network
(AGENTS.md §5.3). This page shows how the pattern catalog does it.

## The rule-based model: `kit.Brain`

`demo/3_adk2_patterns/internal/kit` implements `model.LLM` with a function:

```go
type Brain func(kit.Prompt) kit.Reply

// Prompt: Agent, Text (latest user-side text), Results (function responses by
// tool name), Called (tool names already requested).
// Reply: kit.Say(format, ...) | kit.Call(tool, args) | kit.Reply{Err: err}
```

A Brain decides from the request, not from a replay position. So:

- one agent answers **any** input (works in `go run . console` too);
- concurrent branches cannot steal each other's scripted turns;
- a loop that runs longer than expected does not exhaust a script.

Give each agent its own model: `m.For("agent_name", brain)`. In `-live` mode
`kit.Models` returns the real model and the brain is ignored — so the agent's
`Instruction` must still be a real prompt.

Typical brain for a tool-using agent:

```go
func brain(p kit.Prompt) kit.Reply {
	if _, ok := p.Result("get_order"); !ok {
		return kit.Call("get_order", map[string]any{"order_id": "ORD-42"})
	}
	o, _ := p.Result("get_order")
	return kit.Say("Order is %v.", o["status"])
}
```

Simulate a provider outage with `kit.Reply{Err: errors.New("503")}`.

For a replayed, ordered script instead, the root module has
`internal/fakellm` (`fakellm.New(name, turns...)`, `ErrScriptExhausted`).

## Running an agent: `kit.Run`

```go
tr, err := kit.Run(ctx, agent, io.Discard, "input", answers...)
// tr.Final   last text or string node output
// tr.Calls   every function call name, in order; tr.Called("x")
// tr.Pauses  turns that ended waiting for a human
// tr.Events  full event stream
```

`answers` feed human pauses in order:

- `adk_request_input` → the answer is sent as `{"response": answer}`;
- `adk_request_confirmation` → the answer must be a `bool`, sent as `{"confirmed": b}`.

No answer left → the run stops parked. Assert that nothing irreversible
happened in that case.

## Test shape

Table-driven, parallel, one row per path through the graph:

```go
func TestRoutes(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, input, want string }{
		{"bug", "The app crashes", "bug_desk"},
		{"default catches the rest", "invoice please", "human_triage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := build(kit.Offline())
			if err != nil {
				t.Fatal(err)
			}
			tr, err := kit.Run(context.Background(), a, io.Discard, tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(tr.Final, tt.want) {
				t.Errorf("final = %q, want %s", tr.Final, tt.want)
			}
		})
	}
}
```

Build a fresh agent per subtest: agents and brains hold no shared state then.

Also test the guards: build a deliberately broken graph and assert the error:

```go
if _, err := workflow.New("bad", edges); !errors.Is(err, workflow.ErrMultipleDefaultRoutes) {
	t.Fatalf("err = %v", err)
}
```

And the entry point: `kit.Execute(ctx, spec, nil, &buf)` runs the scripted demo
exactly as `go run .` does.

## Coverage gate

Each package must reach **85%**, `func main()` excluded (AGENTS.md §4):

```bash
sh ./scripts/covgate.sh 85 ./demo/3_adk2_patterns/
task cover
```

Never lower `COVER_MIN`. If 85% is impossible, name the uncovered lines.
Run with `-race`: fan-out branches and dynamic children run concurrently.
