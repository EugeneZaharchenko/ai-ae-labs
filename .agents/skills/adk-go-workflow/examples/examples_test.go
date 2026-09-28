package examples

import (
	"context"
	"iter"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// silent is a model.LLM that is never called; it only lets the coordinator build.
type silent struct{}

func (silent) Name() string { return "silent" }
func (silent) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(func(*model.LLMResponse, error) bool) {}
}

// Every example must pass graph validation — the build is the first test.
func TestExamplesBuild(t *testing.T) {
	t.Parallel()
	builders := map[string]func() (agent.Agent, error){
		"function graph": NewFunctionGraph,
		"hitl":           NewApprovalWorkflow,
		"coordinator":    func() (agent.Agent, error) { return NewCoordinator(silent{}, silent{}) },
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := build(); err != nil {
				t.Fatalf("build: %v", err)
			}
		})
	}
}

func TestFunctionGraphRuns(t *testing.T) {
	t.Parallel()
	tests := []struct{ input, want string }{
		{"ORD-1", "ship: ORD-1 after 3 attempt(s)"},
		{"", "backorder:  after 5 attempt(s)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			a, err := NewFunctionGraph()
			if err != nil {
				t.Fatal(err)
			}
			r, err := runner.New(runner.Config{AppName: "ex", Agent: a, SessionService: session.InMemoryService(), AutoCreateSession: true})
			if err != nil {
				t.Fatal(err)
			}
			var last string
			for ev, err := range r.Run(context.Background(), "u", "s", genai.NewContentFromText(tt.input, genai.RoleUser), agent.RunConfig{}) {
				if err != nil {
					t.Fatal(err)
				}
				if s, ok := ev.Output.(string); ok {
					last = s
				}
			}
			if !strings.HasPrefix(last, tt.want) {
				t.Errorf("final = %q, want %q", last, tt.want)
			}
		})
	}
}
