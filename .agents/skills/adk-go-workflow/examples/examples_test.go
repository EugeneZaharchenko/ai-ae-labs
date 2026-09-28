package examples

import (
	"context"
	"iter"
	"slices"
	"strings"
	"sync"
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

// quiet is a ModelFor that hands the same never-called model to every agent.
// A build test only needs the constructors to accept it, not to run it.
func quiet(string) model.LLM { return silent{} }

// Every example must pass graph validation — the build is the first test.
func TestExamplesBuild(t *testing.T) {
	t.Parallel()
	builders := map[string]func() (agent.Agent, error){
		"function graph": NewFunctionGraph,
		"hitl":           NewApprovalWorkflow,
		"coordinator":    func() (agent.Agent, error) { return NewCoordinator(silent{}, silent{}) },
		"prebuilt sequential": func() (agent.Agent, error) {
			return NewSequentialPipeline(quiet, "write", "review")
		},
		"prebuilt parallel": func() (agent.Agent, error) {
			return NewParallelResearch(quiet, "a", "b")
		},
		"prebuilt loop": func() (agent.Agent, error) {
			return NewRefinementLoop(quiet, 3, "critic", "refiner")
		},
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

// The three prebuilt orchestrators are the only style with no graph assembly,
// so pin their behaviour: order for sequential, the iteration cap for loop, and
// that every parallel branch runs. An uncapped loop is an infinite test, so the
// cap is what makes this runnable at all.
func TestPrebuiltAgentsRun(t *testing.T) {
	t.Parallel()

	t.Run("sequential keeps list order", func(t *testing.T) {
		t.Parallel()
		rec := &recording{}
		a, err := NewSequentialPipeline(rec.modelFor, "first", "second", "third")
		if err != nil {
			t.Fatal(err)
		}
		if got := runToFinal(t, a); got != "from third" {
			t.Errorf("final = %q, want the last stage %q", got, "from third")
		}
		if want := []string{"first", "second", "third"}; !slices.Equal(rec.seen(), want) {
			t.Errorf("stages ran %v, want %v", rec.seen(), want)
		}
	})

	t.Run("loop stops at MaxIterations", func(t *testing.T) {
		t.Parallel()
		rec := &recording{}
		a, err := NewRefinementLoop(rec.modelFor, 3, "stage")
		if err != nil {
			t.Fatal(err)
		}
		runToFinal(t, a)
		if got := rec.seen(); len(got) != 3 {
			t.Errorf("stage ran %d time(s) %v, want exactly MaxIterations=3", len(got), got)
		}
	})

	t.Run("parallel runs every branch", func(t *testing.T) {
		t.Parallel()
		rec := &recording{}
		a, err := NewParallelResearch(rec.modelFor, "left", "right")
		if err != nil {
			t.Fatal(err)
		}
		runToFinal(t, a)
		got := rec.seen()
		if len(got) != 2 {
			t.Fatalf("branches ran %v, want both once", got)
		}
		slices.Sort(got)
		if want := []string{"left", "right"}; !slices.Equal(got, want) {
			t.Errorf("branches ran %v, want %v", got, want)
		}
	})
}

// recording hands out one model.LLM per agent name and notes which models were
// actually called. The name arrives from the ModelFor factory, not from the
// request — a request carries the previous stage's text, not the caller's name,
// so the factory is the only reliable way to attribute a call to an agent.
type recording struct {
	mu   sync.Mutex
	used []string
}

// modelFor returns the model for one agent; it records the first call per name.
func (r *recording) modelFor(name string) model.LLM {
	return recordingModel{rec: r, name: name}
}

func (r *recording) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.used)
}

func (r *recording) note(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.used = append(r.used, name)
}

type recordingModel struct {
	rec  *recording
	name string
}

func (m recordingModel) Name() string { return m.name }

func (m recordingModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.rec.note(m.name)
		yield(&model.LLMResponse{Content: genai.NewContentFromText("from "+m.name, genai.RoleModel)}, nil)
	}
}

// runToFinal drives an agent to completion and returns its last text output.
func runToFinal(t *testing.T, a agent.Agent) string {
	t.Helper()
	r, err := runner.New(runner.Config{AppName: "ex", Agent: a, SessionService: session.InMemoryService(), AutoCreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	var last string
	for ev, err := range r.Run(context.Background(), "u", "s", genai.NewContentFromText("go", genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Content != nil {
			for _, p := range ev.Content.Parts {
				if p.Text != "" {
					last = p.Text
				}
			}
		}
	}
	return last
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
