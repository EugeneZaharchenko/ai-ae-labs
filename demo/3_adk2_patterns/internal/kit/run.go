package kit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"
)

// Trace is everything a test asserts about one demo run.
type Trace struct {
	// Final is the last text or string output of the run.
	Final string
	// Events is every event of every turn, in order.
	Events []*session.Event
	// Calls names every function call, in order.
	Calls []string
	// Pauses counts turns that ended waiting for a human.
	Pauses int
}

// Called reports whether the named function was called.
func (t Trace) Called(name string) bool {
	for _, c := range t.Calls {
		if c == name {
			return true
		}
	}
	return false
}

// Run executes a in a fresh in-memory session and prints the trace to out.
//
// answers feed human-in-the-loop pauses, one per pause, in order: a workflow
// input request (adk_request_input) receives the answer as its response; a
// tool confirmation (adk_request_confirmation) needs a bool. A pause with no
// answer left ends the run — that is what "the workflow parked" looks like.
func Run(ctx context.Context, a agent.Agent, out io.Writer, input string, answers ...any) (Trace, error) {
	svc := session.InMemoryService()
	r, err := runner.New(runner.Config{
		AppName:           "adk2_patterns",
		Agent:             a,
		SessionService:    svc,
		AutoCreateSession: true,
	})
	if err != nil {
		return Trace{}, fmt.Errorf("build runner: %w", err)
	}

	var tr Trace
	msg := genai.NewContentFromText(input, genai.RoleUser)
	fmt.Fprintf(out, "👤 user: %s\n", input)
	for {
		var pending *genai.FunctionCall
		for ev, runErr := range r.Run(ctx, "user", "demo", msg, agent.RunConfig{}) {
			if runErr != nil {
				return tr, fmt.Errorf("run %s: %w", a.Name(), runErr)
			}
			tr.Events = append(tr.Events, ev)
			if fc := record(&tr, ev, out); fc != nil {
				pending = fc
			}
		}
		if pending == nil {
			return tr, nil
		}
		tr.Pauses++
		if len(answers) == 0 {
			fmt.Fprintln(out, "⏸  paused: waiting for a human (no scripted answer left)")
			return tr, nil
		}
		msg, err = answer(pending, answers[0])
		if err != nil {
			return tr, err
		}
		fmt.Fprintf(out, "👤 human answers %s: %v\n", pending.Name, answers[0])
		answers = answers[1:]
	}
}

// answer builds the FunctionResponse that resumes a paused run.
func answer(fc *genai.FunctionCall, v any) (*genai.Content, error) {
	resp := map[string]any{"response": v}
	if fc.Name == toolconfirmation.FunctionCallName {
		ok, isBool := v.(bool)
		if !isBool {
			return nil, fmt.Errorf("confirmation answer must be bool, got %T", v)
		}
		resp = map[string]any{"confirmed": ok}
	}
	part := &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: fc.ID, Name: fc.Name, Response: resp}}
	return &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{part}}, nil
}

// record prints one event, updates the trace and returns a pending
// human-input call if the event carries one.
func record(tr *Trace, ev *session.Event, out io.Writer) *genai.FunctionCall {
	var pending *genai.FunctionCall
	who := ev.Author
	if who == "" {
		who = "workflow"
	}
	// Node events carry "parent/child@run"; the leaf is the node that spoke.
	if ev.NodeInfo != nil && ev.NodeInfo.Path != "" {
		who = leafNode(ev.NodeInfo.Path)
	}
	if ev.Content != nil {
		for _, p := range ev.Content.Parts {
			switch {
			case p == nil:
			case p.FunctionCall != nil:
				fc := p.FunctionCall
				tr.Calls = append(tr.Calls, fc.Name)
				if fc.Name == workflow.WorkflowInputFunctionCallName || fc.Name == toolconfirmation.FunctionCallName {
					pending = fc
					fmt.Fprintf(out, "🙋 %s asks a human: %s\n", who, humanPrompt(fc))
					continue
				}
				fmt.Fprintf(out, "🔧 %s → %s(%s)\n", who, fc.Name, compact(fc.Args))
			case p.FunctionResponse != nil:
				fmt.Fprintf(out, "📦 %s ← %s = %s\n", who, p.FunctionResponse.Name, compact(p.FunctionResponse.Response))
			case p.Text != "" && !p.Thought:
				tr.Final = strings.TrimSpace(p.Text)
				fmt.Fprintf(out, "🤖 %s: %s\n", who, tr.Final)
			}
		}
	}
	if ev.Output != nil && (ev.Content == nil || len(ev.Content.Parts) == 0) {
		if s, ok := ev.Output.(string); ok {
			tr.Final = s
		} else {
			tr.Final = compact(ev.Output)
		}
		fmt.Fprintf(out, "⚙️  %s ⇒ %s\n", who, compact(ev.Output))
	}
	if len(ev.Routes) > 0 {
		fmt.Fprintf(out, "🔀 %s route=%v\n", who, ev.Routes)
	}
	return pending
}

// leafNode returns the last segment of a node path with its run id dropped:
// "wf/wf@1/approve@2" → "approve", "classify@1" → "classify".
func leafNode(path string) string {
	leaf := path[strings.LastIndexByte(path, '/')+1:]
	if i := strings.IndexByte(leaf, '@'); i >= 0 {
		leaf = leaf[:i]
	}
	return leaf
}

func humanPrompt(fc *genai.FunctionCall) string {
	if m, ok := fc.Args["message"].(string); ok && m != "" {
		return m
	}
	if tc, ok := fc.Args["toolConfirmation"].(map[string]any); ok {
		if h, ok := tc["hint"].(string); ok {
			return h
		}
	}
	return compact(fc.Args)
}

// compact renders a value as one-line JSON, truncated for the terminal.
func compact(v any) string {
	if s, ok := v.(string); ok {
		return clip(s)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return clip(fmt.Sprint(v))
	}
	return clip(string(b))
}

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 160
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
