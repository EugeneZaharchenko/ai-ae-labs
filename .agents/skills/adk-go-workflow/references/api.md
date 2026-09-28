# ADK Go v2.4.0 — Workflow API Cheat Sheet

Every identifier below was read from `google.golang.org/adk/v2@v2.4.0` source
(станом на 09/2026). If you use a different version, re-check before you copy.

## Imports

```go
import (
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"   // graph → agent.Agent
	// Prebuilt orchestrators. Note the PLURAL directory.
	"google.golang.org/adk/v2/agent/workflowagents/loopagent"
	"google.golang.org/adk/v2/agent/workflowagents/parallelagent"
	"google.golang.org/adk/v2/agent/workflowagents/sequentialagent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/agenttool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/adk/v2/workflow"
)
```

## The three workflow styles

ADK composes multi-step work three ways. Pick the style before the pattern.

| Style | Entry point | Notes |
|---|---|---|
| Graph-based | `workflow.New` / `workflowagent.New` | Declarative nodes + edges, explicit routing. Deterministic, structured. |
| Dynamic | `workflow.NewDynamicNode` + `workflow.RunNode` | Plain Go loops, conditionals, recursion. For control flow a static graph cannot express. |
| Prebuilt | `sequentialagent.New`, `parallelagent.New`, `loopagent.New` | No graph assembly at all. See [Prebuilt workflow agents](#prebuilt-workflow-agents). |

They nest: a graph node can wrap a prebuilt agent (`NewAgentNode`), and a
dynamic node can drive any of them via `RunNode`.

## Graph construction

| API | Notes |
|---|---|
| `workflow.Start` | The entry node. Must have no incoming edges. |
| `workflow.Chain(nodes...) []Edge` | Linear edges: `Chain(workflow.Start, a, b, c)`. |
| `workflow.NewEdgeBuilder()` | Fluent builder; finish with `.Build() []Edge`. |
| `.Add(from, to)` | Unconditional edge. |
| `.AddFanOut(from, to...)` | One node → several; branches run concurrently. |
| `.AddFanIn(to, from...)` | Several → one; `to` **must** be a `*JoinNode`. |
| `.AddRoute(from, to, route)` | Conditional edge. |
| `.AddRoutes(from, map[string]Node)` | One `StringRoute` edge per key. |
| `workflow.New(name, edges, opts...)` | Validates and returns `*Workflow`. Options: `WithMaxConcurrency(n)`, `WithStateSchema(s)`, `WithRootWrapper()`. |
| `workflowagent.New(workflowagent.Config{Name, Description, Edges})` | The graph as an `agent.Agent` (runner, launcher, Web UI). No option for max concurrency. |
| `workflow.NewWorkflowNode(name, edges)` | A nested graph as a node. The name must differ from the parent's. |

## Nodes

| Constructor | Body |
|---|---|
| `NewFunctionNode[IN, OUT](name, fn, cfg)` | `func(agent.Context, IN) (OUT, error)` |
| `NewEmittingFunctionNode[IN, OUT](name, fn, cfg)` | `func(ctx agent.Context, in IN, emit func(*session.Event) error) (OUT, error)` — can emit HITL requests and progress. |
| `NewJoinNode(name)` | Barrier. Output is `map[string]any` keyed by predecessor name. |
| `NewAgentNode(agent, cfg)` | Wraps an agent. An `llmagent` with unset `Mode` runs single-turn here; its text becomes the node output. |
| `NewToolNode(tool, cfg)` | A tool as a node. |
| `NewDynamicNode[IN, OUT](name, fn, cfg) Node` | `fn` is `DynamicFn[IN, OUT]` = same shape as the emitting body. Returns `Node`, not a named type. `RerunOnResume` defaults to `true`. |
| `NewParallelWorker(name, wrapped, maxConcurrency, cfg)` | Runs `wrapped` once per item of a list input. |

`NodeConfig` fields: `RerunOnResume *bool`, `WaitForOutput *bool`,
`RetryConfig *RetryConfig` (`workflow.DefaultRetryConfig()`), `Timeout time.Duration`,
`ParallelWorker bool`.

## Routing

A node routes by emitting an event with `Routes` set. A `FunctionNode` whose
`OUT` is `*session.Event` yields that event as-is:

```go
func dispatch(ctx agent.Context, in string) (*session.Event, error) {
	ev := session.NewEvent(ctx, ctx.InvocationID())
	ev.Output = in
	ev.Routes = []string{"BUG"} // leave empty → only Default matches
	return ev, nil
}
```

| Route type | Matches when `ev.Routes` contains |
|---|---|
| `StringRoute("x")` | `"x"` |
| `BoolRoute(true)` | `fmt.Sprint(true)` → `"true"` |
| `IntRoute(3)` | `"3"` |
| `MultiRoute[T]{a, b}` | any of the values |
| `workflow.Default` | no other route of this node matched — a value, not a string |

## Dynamic nodes

```go
node := workflow.NewDynamicNode("orchestrate",
	func(ctx agent.Context, in string, emit func(*session.Event) error) (string, error) {
		for i := range maxSteps { // your cap
			out, err := workflow.RunNode[string](ctx, child, in, workflow.WithRunID(fmt.Sprintf("step-%d", i)))
			if err != nil {
				return "", err
			}
			in = out
		}
		return in, nil
	}, workflow.NodeConfig{})
```

`RunNode` options: `WithRunID(id)` (idempotent replay on resume),
`WithUseSubBranch()` (isolated branch), `WithUseAsOutput()`,
`WithIsolationScope(s)`, `WithOverrideBranch(b)`, `WithRaiseOnWait()`.
`RunNode` outside a dynamic body → `ErrInvalidRunNodeContext`.

## Agents

| API | Notes |
|---|---|
| `llmagent.New(llmagent.Config{...})` | `Name, Description, Model, Instruction, Tools, Toolsets, SubAgents, OutputKey, OutputSchema, InputSchema, IncludeContents, Mode`, callbacks incl. `BeforeToolCallbacks`. |
| `llmagent.ModeChat` | Reached via `transfer_to_agent`. Default for a sub-agent. |
| `llmagent.ModeSingleTurn` | Sub-agent exposed to the parent as a **tool named after the agent** with args `{"request": string}`. Own isolated turn. |
| `llmagent.ModeTask` | Multi-turn task sub-agent. Forbidden as a static graph node. |
| `agenttool.New(agent, *agenttool.Config)` | Agent as a tool. Args `{"request": string}` unless the agent has an `InputSchema`. `Config{SkipSummarization}`. |
| `functiontool.New(functiontool.Config{Name, Description}, handler)` | `handler func(agent.Context, TArgs) (TResults, error)`; schema is inferred from the Go types. |
| `loopagent.New`, `sequentialagent.New`, `parallelagent.New` | Prebuilt workflow agents (the third style). Still present and working in v2.4.0 despite the docs calling template workflows "superseded". See below. |

## Prebuilt workflow agents

All three live under `agent/workflowagents/` and return `(agent.Agent, error)`.
Each takes `Config{AgentConfig: agent.Config{Name, Description, SubAgents}}`.
None of them accepts a custom `Run` — passing one is an error.

```go
seq, err := sequentialagent.New(sequentialagent.Config{
	AgentConfig: agent.Config{Name: "pipeline", SubAgents: []agent.Agent{a, b}},
})

par, err := parallelagent.New(parallelagent.Config{
	AgentConfig: agent.Config{Name: "research", SubAgents: []agent.Agent{x, y}},
})

loop, err := loopagent.New(loopagent.Config{
	AgentConfig:   agent.Config{Name: "refine", SubAgents: []agent.Agent{critic, refiner}},
	MaxIterations: 5,
})
```

| Agent | Runs | Cap / termination |
|---|---|---|
| `sequentialagent` | Sub-agents once each, in list order. | None needed. Last sub-agent's text is the result. |
| `parallelagent` | Sub-agents concurrently, each in an isolated branch. | Waits for all branches. **No fan-in.** |
| `loopagent` | The whole sub-agent list, repeatedly. | `MaxIterations uint`; also stops on any sub-agent's `Escalate`. |

`loopagent` specifics, read from the v2.4.0 source:

- `MaxIterations: 0` **loops forever** (until an escalation). It is not "skip".
- The cap is checked *after* a full pass, so `MaxIterations: 3` runs the list
  exactly 3 times.
- Early exit is `ctx.Actions().Escalate = true` from a tool body; the loop
  inspects `event.Actions.Escalate` after each sub-agent and returns.

`parallelagent` specifics:

- Each branch gets its own `InvocationContext` with a distinct `Branch` name
  (`parent.child`), so branches cannot read each other's state.
- Results arrive in completion order, not list order.
- To merge branch output, add a following agent — or use the graph form
  (`AddFanOut` + `NewJoinNode`), which joins by construction.

Naming trap: `agent/workflowagent` (**singular**, no `s`) is a different
package — it adapts a `*workflow.Workflow` graph into an `agent.Agent`. The
three prebuilt agents are in `agent/workflowagents/` (**plural**).

## Human in the loop

**Workflow input (E1).** Inside an emitting node:

```go
reply, err := workflow.ResumeOrRequestInput(ctx, emit, session.RequestInput{
	InterruptID: "approve-" + ctx.InvocationID(),
	Message:     "Approve?",
	Payload:     details,
})
if err != nil {
	return zero, err // ErrNodeInterrupted on the first pass → the run parks
}
```

The node needs `NodeConfig{RerunOnResume: &true}`. The client resumes with a
user message that holds a `FunctionResponse`:

```go
&genai.FunctionResponse{
	ID:       pendingCall.ID,                        // the InterruptID
	Name:     workflow.WorkflowInputFunctionCallName, // "adk_request_input"
	Response: map[string]any{"response": "approve"},  // or {"payload": v}
}
```

A `"response"` string is parsed as JSON when it is valid JSON.

**Tool confirmation (E2).**

```go
functiontool.Config{
	Name: "delete_bucket",
	RequireConfirmationProvider: func(in deleteArgs) bool {
		return strings.HasPrefix(in.Bucket, "prod-")
	},
}
```

The runtime emits a call named `toolconfirmation.FunctionCallName`
(`"adk_request_confirmation"`). Resume with a `FunctionResponse` with the same
ID and name and `Response: map[string]any{"confirmed": true}`. Inside a tool,
`ctx.RequestConfirmation(hint, payload)` and `ctx.ToolConfirmation()` give manual control.

## Running

```go
r, err := runner.New(runner.Config{AppName: "app", Agent: a,
	SessionService: session.InMemoryService(), AutoCreateSession: true})
for ev, err := range r.Run(ctx, userID, sessionID, genai.NewContentFromText(in, genai.RoleUser), agent.RunConfig{}) {
	// ev.Content (model text, calls), ev.Output (node output), ev.Routes
}
```

Launcher (console / Web UI / REST): `full.NewLauncher().Execute(ctx,
&launcher.Config{AgentLoader: agent.NewSingleLoader(a)}, args)`.
