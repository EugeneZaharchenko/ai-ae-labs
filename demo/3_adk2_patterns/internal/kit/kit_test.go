package kit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"
)

func echoAgent(t *testing.T, m Models, brain Brain) agent.Agent {
	t.Helper()
	a, err := llmagent.New(llmagent.Config{Name: "echo", Model: m.For("echo", brain), Instruction: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRead(t *testing.T) {
	t.Parallel()
	turn := []*genai.Content{
		nil,
		genai.NewContentFromText("  first  ", genai.RoleUser),
		{Role: genai.RoleModel, Parts: []*genai.Part{nil, genai.NewPartFromFunctionCall("lookup", nil), genai.NewPartFromText("model talk")}},
		{Role: genai.RoleUser, Parts: []*genai.Part{genai.NewPartFromFunctionResponse("lookup", map[string]any{"v": 1})}},
	}
	p := Read("a", &model.LLMRequest{Contents: turn})
	if p.Text != "first" {
		t.Errorf("Text = %q, want latest user text", p.Text)
	}
	if len(p.Called) != 1 || p.Called[0] != "lookup" {
		t.Errorf("Called = %v", p.Called)
	}
	if r, ok := p.Result("lookup"); !ok || r["v"] != 1 {
		t.Errorf("Result = %v %v", r, ok)
	}

	// A second user message in the same session starts a fresh turn: the
	// first turn's tool results must not leak into it (console sessions).
	next := Read("a", &model.LLMRequest{Contents: append(turn, genai.NewContentFromText("second", genai.RoleUser))})
	if _, ok := next.Result("lookup"); ok || len(next.Called) != 0 || next.Text != "second" {
		t.Errorf("previous turn leaked: %+v", next)
	}
	if !p.Has("FIR", "zzz") || p.Has("zzz") {
		t.Error("Has is not a case-insensitive any-match")
	}
	if got := Read("a", nil); got.Text != "" || got.Agent != "a" {
		t.Errorf("Read(nil) = %+v", got)
	}
}

func TestModel(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name    string
		reply   Reply
		wantErr error
		check   func(*genai.Part) bool
	}{
		{"text", Say("hi %d", 1), nil, func(p *genai.Part) bool { return p.Text == "hi 1" }},
		{"call", Call("f", map[string]any{"x": 1}), nil, func(p *genai.Part) bool { return p.FunctionCall != nil && p.FunctionCall.Name == "f" }},
		{"error", Reply{Err: boom}, boom, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := NewModel("x", func(Prompt) Reply { return tt.reply })
			if m.Name() != "offline/x" {
				t.Errorf("Name = %q", m.Name())
			}
			for resp, err := range m.GenerateContent(context.Background(), &model.LLMRequest{}, false) {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				if tt.check != nil && !tt.check(resp.Content.Parts[0]) {
					t.Errorf("unexpected part %+v", resp.Content.Parts[0])
				}
			}
			if m.Calls() != 1 {
				t.Errorf("Calls = %d", m.Calls())
			}
		})
	}
}

func TestModels(t *testing.T) {
	t.Parallel()
	if Offline().IsLive() {
		t.Error("Offline is live")
	}
	live := NewModel("real", nil)
	m := Live(live)
	if !m.IsLive() || m.For("any", nil) != live {
		t.Error("Live must hand out the live model")
	}
	if mode(m) != "live" || mode(Offline()) == "live" {
		t.Error("mode label")
	}
}

func TestRunChat(t *testing.T) {
	t.Parallel()
	a := echoAgent(t, Offline(), func(p Prompt) Reply { return Say("you said %s", p.Text) })
	var b strings.Builder
	tr, err := Run(context.Background(), a, &b, "ping")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Final != "you said ping" || tr.Called("x") {
		t.Errorf("trace = %+v", tr)
	}
	if !strings.Contains(b.String(), "🤖 echo: you said ping") {
		t.Errorf("printed:\n%s", b.String())
	}
}

func TestRunModelError(t *testing.T) {
	t.Parallel()
	a := echoAgent(t, Offline(), func(Prompt) Reply { return Reply{Err: errors.New("down")} })
	if _, err := Run(context.Background(), a, &strings.Builder{}, "x"); err == nil {
		t.Fatal("want the model error surfaced")
	}
}

type lookupArgs struct {
	Key string `json:"key"`
}

// One graph that exercises every trace line: tool call, tool result, route,
// node output and a human pause, answered or left parked.
func TestRunToolsRoutesAndPauses(t *testing.T) {
	t.Parallel()
	lookup, err := functiontool.New(functiontool.Config{Name: "lookup", Description: "d"},
		func(_ agent.Context, in lookupArgs) (map[string]any, error) { return map[string]any{"v": in.Key}, nil })
	if err != nil {
		t.Fatal(err)
	}
	llm, err := llmagent.New(llmagent.Config{
		Name: "asker",
		Model: NewModel("asker", func(p Prompt) Reply {
			if _, ok := p.Result("lookup"); !ok {
				return Call("lookup", map[string]any{"key": "k"})
			}
			return Say("looked")
		}),
		Tools: []tool.Tool{lookup},
	})
	if err != nil {
		t.Fatal(err)
	}
	ask, err := workflow.NewAgentNode(llm, workflow.NodeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	route := workflow.NewFunctionNode("route", func(ctx agent.Context, in any) (*session.Event, error) {
		ev := session.NewEvent(ctx, ctx.InvocationID())
		ev.Output = map[string]any{"in": in}
		ev.Routes = []string{"go"}
		return ev, nil
	}, workflow.NodeConfig{})
	rerun := true
	gate := workflow.NewEmittingFunctionNode("gate", func(ctx agent.Context, _ any, emit func(*session.Event) error) (string, error) {
		reply, err := workflow.ResumeOrRequestInput(ctx, emit, session.RequestInput{InterruptID: "g-" + ctx.InvocationID(), Message: "ok?"})
		if err != nil {
			return "", err
		}
		return fmt.Sprint("human:", reply), nil
	}, workflow.NodeConfig{RerunOnResume: &rerun})
	a, err := workflowagent.New(workflowagent.Config{Name: "wf", Edges: workflow.NewEdgeBuilder().
		Add(workflow.Start, ask).Add(ask, route).AddRoute(route, gate, workflow.StringRoute("go")).Build()})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		answers []any
		want    string
	}{
		{"answered", []any{"yes"}, "human:yes"},
		{"parked", nil, "paused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var b strings.Builder
			tr, err := Run(context.Background(), a, &b, "go", tt.answers...)
			if err != nil {
				t.Fatal(err)
			}
			if !tr.Called("lookup") || tr.Called("nope") || tr.Pauses != 1 {
				t.Errorf("calls=%v pauses=%d", tr.Calls, tr.Pauses)
			}
			for _, w := range []string{"🔧", "📦", "🔀", "🙋", tt.want} {
				if !strings.Contains(b.String(), w) {
					t.Errorf("trace misses %q:\n%s", w, b.String())
				}
			}
		})
	}
}

func TestAnswer(t *testing.T) {
	t.Parallel()
	in := &genai.FunctionCall{ID: "1", Name: workflow.WorkflowInputFunctionCallName}
	c, err := answer(in, "yes")
	if err != nil || c.Parts[0].FunctionResponse.Response["response"] != "yes" {
		t.Fatalf("input answer = %+v, %v", c, err)
	}
	conf := &genai.FunctionCall{ID: "2", Name: toolconfirmation.FunctionCallName}
	c, err = answer(conf, true)
	if err != nil || c.Parts[0].FunctionResponse.Response["confirmed"] != true {
		t.Fatalf("confirmation answer = %+v, %v", c, err)
	}
	if _, err := answer(conf, "yes"); err == nil {
		t.Fatal("non-bool confirmation must fail")
	}
}

func TestHelpers(t *testing.T) {
	t.Parallel()
	if got := leafNode("wf/wf@1/approve@2"); got != "approve" {
		t.Errorf("leafNode = %q", got)
	}
	if got := leafNode("classify"); got != "classify" {
		t.Errorf("leafNode = %q", got)
	}
	if got := compact(map[string]any{"a": 1}); got != `{"a":1}` {
		t.Errorf("compact = %q", got)
	}
	if got := compact(func() {}); got == "" {
		t.Error("compact of an unmarshalable value must still render")
	}
	if got := clip(strings.Repeat("x", 500)); len([]rune(got)) != 161 {
		t.Errorf("clip len = %d", len([]rune(got)))
	}
	msg := &genai.FunctionCall{Args: map[string]any{"message": "ok?"}}
	hint := &genai.FunctionCall{Args: map[string]any{"toolConfirmation": map[string]any{"hint": "sure?"}}}
	other := &genai.FunctionCall{Args: map[string]any{"z": 1}}
	if humanPrompt(msg) != "ok?" || humanPrompt(hint) != "sure?" || humanPrompt(other) != `{"z":1}` {
		t.Error("humanPrompt")
	}
}

func TestExecute(t *testing.T) {
	t.Parallel()
	build := func(m Models) (agent.Agent, error) {
		return llmagent.New(llmagent.Config{Name: "echo", Model: m.For("echo", func(p Prompt) Reply { return Say("ok %s", p.Text) })})
	}
	tests := []struct {
		name    string
		spec    Spec
		args    []string
		want    string
		wantErr bool
	}{
		{"scripted", Spec{ID: "T", Build: build, Input: "a"}, nil, "ok a", false},
		{"input flag", Spec{ID: "T", Build: build}, []string{"-input", "b"}, "ok b", false},
		{"custom demo", Spec{ID: "T", Demo: func(_ context.Context, _ Models, out io.Writer) error {
			_, err := out.Write([]byte("custom"))
			return err
		}}, nil, "custom", false},
		{"bad flag", Spec{ID: "T", Build: build}, []string{"-nope"}, "", true},
		{"build error", Spec{ID: "T", Build: func(Models) (agent.Agent, error) { return nil, errors.New("x") }}, nil, "", true},
		{"launcher build error", Spec{ID: "T", Build: func(Models) (agent.Agent, error) { return nil, errors.New("x") }}, []string{"console"}, "", true},
		{"launcher bad args", Spec{ID: "T", Build: build}, []string{"no-such-launcher"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var b strings.Builder
			err := Execute(context.Background(), tt.spec, tt.args, &b)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !strings.Contains(b.String(), tt.want) {
				t.Errorf("output misses %q:\n%s", tt.want, b.String())
			}
		})
	}
}
