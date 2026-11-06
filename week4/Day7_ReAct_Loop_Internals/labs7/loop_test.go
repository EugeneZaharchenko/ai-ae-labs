package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/ledger"
	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/react"
)

// funcPlanner turns a function into a Planner; n is the 1-based step number.
type funcPlanner struct {
	n  int
	fn func(n int, h react.History) (react.Step, error)
}

func (f *funcPlanner) NextStep(_ context.Context, h react.History) (react.Step, error) {
	f.n++
	return f.fn(f.n, h)
}

// sameCall asks for the same search every time, only the CallID changes.
func sameCall() *funcPlanner {
	return &funcPlanner{fn: func(n int, _ react.History) (react.Step, error) {
		return react.Step{Thought: "search again", Calls: []react.ToolCall{{
			ID: fmt.Sprintf("c%d", n), Name: "search_internal_wiki", Args: map[string]any{"query": "payout"}}}}, nil
	}}
}

func emptyStep() *funcPlanner {
	return &funcPlanner{fn: func(int, react.History) (react.Step, error) {
		return react.Step{Thought: "thinking, but doing nothing"}, nil
	}}
}

// counted is a read-only tool that always returns the same value and counts calls.
func counted(n *int) react.Registry {
	return react.NewRegistry(react.Tool{Name: "search_internal_wiki", Call: func(context.Context, map[string]any) (string, error) {
		*n++
		return "3 hits", nil
	}})
}

// runGraphOnce runs the graph version once and returns its answer and events.
func runGraphOnce(t *testing.T, p react.Planner, reg react.Registry, b react.Budget) (labrun.Result, error) {
	t.Helper()
	a, err := NewGraphAgent(p, reg, b)
	if err != nil {
		t.Fatalf("NewGraphAgent: %v", err)
	}
	return labrun.Run(context.Background(), a, defaultTask)
}

func TestRunRuleScenario(t *testing.T) {
	t.Parallel()
	l := ledger.New()
	answer, tr, err := Run(context.Background(), &react.RulePlanner{}, l.Tools(), defaultTask, react.Budget{MaxIterations: maxIterations})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "#418") || !strings.Contains(answer, "LDG-1") {
		t.Errorf("answer = %q", answer)
	}
	if tr.Iterations != 3 || len(tr.Steps) != 3 || len(tr.Observations) != 2 {
		t.Errorf("iterations=%d steps=%d observations=%d, want 3/3/2", tr.Iterations, len(tr.Steps), len(tr.Observations))
	}
	if got := l.Tickets(); len(got) != 1 {
		t.Errorf("tickets = %v, want one", got)
	}
}

// The limit error keeps the trace: it shows where the loop got stuck.
func TestRunLimitKeepsTrace(t *testing.T) {
	t.Parallel()
	_, tr, err := Run(context.Background(), &endless{}, ledger.New().Tools(), "task", react.Budget{MaxIterations: maxIterations})
	if !errors.Is(err, ErrMaxIterations) || !strings.Contains(err.Error(), "вичерпано ліміт 5 ітерацій") {
		t.Fatalf("err = %v", err)
	}
	if tr.Iterations != maxIterations || len(tr.Observations) != maxIterations {
		t.Errorf("trace has %d iterations / %d observations, want %d", tr.Iterations, len(tr.Observations), maxIterations)
	}
}

func TestRunZeroBudgetUsesDefault(t *testing.T) {
	t.Parallel()
	_, tr, err := Run(context.Background(), &endless{}, ledger.New().Tools(), "task", react.Budget{})
	if !errors.Is(err, ErrMaxIterations) || tr.Iterations != react.DefaultMaxIterations {
		t.Fatalf("err = %v, iterations = %d", err, tr.Iterations)
	}
}

func TestRunEmptyStep(t *testing.T) {
	t.Parallel()
	_, _, err := Run(context.Background(), emptyStep(), ledger.New().Tools(), "task", react.Budget{})
	if !errors.Is(err, ErrEmptyStep) {
		t.Fatalf("err = %v, want ErrEmptyStep", err)
	}
}

func TestRunPlannerError(t *testing.T) {
	t.Parallel()
	boom := errors.New("provider down")
	p := &funcPlanner{fn: func(int, react.History) (react.Step, error) { return react.Step{}, boom }}
	if _, _, err := Run(context.Background(), p, ledger.New().Tools(), "task", react.Budget{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunTokenBudget(t *testing.T) {
	t.Parallel()
	p := &funcPlanner{fn: func(n int, _ react.History) (react.Step, error) {
		return react.Step{Thought: "read", Usage: react.Usage{Prompt: 500, Output: 100},
			Calls: []react.ToolCall{{ID: fmt.Sprint(n), Name: "search_internal_wiki", Args: map[string]any{"query": n}}}}, nil
	}}
	// 600 tokens per step: after 2 steps 1200 >= 1000, the 3rd step is not planned.
	_, tr, err := Run(context.Background(), p, ledger.New().Tools(), "task", react.Budget{MaxTokens: 1000})
	if !errors.Is(err, ErrTokenBudget) || tr.Iterations != 2 || tr.Usage.Total() != 1200 {
		t.Fatalf("err = %v, iterations = %d, usage = %d", err, tr.Iterations, tr.Usage.Total())
	}
}

func TestRunDeadline(t *testing.T) {
	t.Parallel()
	slow := react.NewRegistry(react.Tool{Name: "search_internal_wiki", Call: func(ctx context.Context, _ map[string]any) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}})
	_, _, err := Run(context.Background(), &endless{}, slow, "task", react.Budget{MaxDuration: 20 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

// Advanced: the same call with the same args 3 times in a row stops the loop
// before maxIterations. The 3rd call is not executed.
func TestRunLoopDetected(t *testing.T) {
	t.Parallel()
	var calls int
	_, tr, err := Run(context.Background(), sameCall(), counted(&calls), "task", react.Budget{MaxIterations: 5})
	if !errors.Is(err, ErrLoopDetected) {
		t.Fatalf("err = %v, want ErrLoopDetected", err)
	}
	if tr.Iterations != 3 || calls != 2 {
		t.Errorf("stopped at iteration %d after %d tool calls, want 3 and 2", tr.Iterations, calls)
	}
}

func TestLoopGuardResetsOnOtherCall(t *testing.T) {
	t.Parallel()
	a := []react.ToolCall{{ID: "1", Name: "x", Args: map[string]any{"q": 1}}}
	b := []react.ToolCall{{ID: "2", Name: "x", Args: map[string]any{"q": 2}}}
	var g loopGuard
	for i, calls := range [][]react.ToolCall{a, a, b, a, a} {
		if g.repeated(calls) {
			t.Fatalf("step %d counted as a loop", i+1)
		}
	}
	if !g.repeated(a) {
		t.Error("third a in a row must be a loop")
	}
}

// Advanced: safe fan-out. Two reads run at the same time (each one waits for
// the other to start), two writes run one by one in the asked order, and every
// observation is at the index of its call.
func TestDispatchFanOut(t *testing.T) {
	t.Parallel()
	var started sync.WaitGroup
	started.Add(2)
	read := func(name string) react.Tool {
		return react.Tool{Name: name, Call: func(context.Context, map[string]any) (string, error) {
			started.Done()
			done := make(chan struct{})
			go func() { started.Wait(); close(done) }()
			select {
			case <-done:
				return name + " ok", nil
			case <-time.After(time.Second):
				return "", errors.New(name + " ran alone: reads are not parallel")
			}
		}}
	}
	var mu sync.Mutex
	var writes []string
	write := func(name string) react.Tool {
		return react.Tool{Name: name, SideEffecting: true, Call: func(context.Context, map[string]any) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			writes = append(writes, name)
			return name + " ok", nil
		}}
	}
	reg := react.NewRegistry(read("read_a"), read("read_b"), write("write_1"), write("write_2"))
	calls := []react.ToolCall{{ID: "w1", Name: "write_1"}, {ID: "r1", Name: "read_a"}, {ID: "w2", Name: "write_2"}, {ID: "r2", Name: "read_b"}}

	obs := dispatch(context.Background(), reg, calls)
	for i, c := range calls {
		if obs[i].CallID != c.ID || obs[i].Err != nil {
			t.Errorf("obs[%d] = %v, want ok for %s", i, obs[i], c.ID)
		}
	}
	if strings.Join(writes, ",") != "write_1,write_2" {
		t.Errorf("writes ran as %v", writes)
	}
}

func TestDispatchUnknownTool(t *testing.T) {
	t.Parallel()
	obs := dispatch(context.Background(), ledger.New().Tools(), []react.ToolCall{{ID: "c1", Name: "drop_database"}})
	if !errors.Is(obs[0].Err, react.ErrUnknownTool) || obs[0].CallID != "c1" {
		t.Fatalf("obs = %v", obs[0])
	}
}

// --- Graph version -----------------------------------------------------------

func TestGraphRuleScenario(t *testing.T) {
	t.Parallel()
	l := ledger.New()
	res, err := runGraphOnce(t, &react.RulePlanner{}, l.Tools(), react.Budget{MaxIterations: maxIterations})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Final, "#418") || !strings.Contains(res.Final, "LDG-1") {
		t.Errorf("final = %q", res.Final)
	}
	if got := l.Tickets(); len(got) != 1 {
		t.Errorf("tickets = %v, want one", got)
	}
	// One event per child node run: 3 plan_step + 2 call_tools.
	count := map[string]int{}
	for _, e := range res.Events {
		if e.NodeInfo != nil {
			path := strings.Split(e.NodeInfo.Path, "/")
			count[strings.Split(path[len(path)-1], "@")[0]]++
		}
	}
	if count["plan_step"] != 3 || count["call_tools"] != 2 {
		t.Errorf("events per node = %v (total %d)", count, len(res.Events))
	}
}

func TestGraphPrintsTheLoop(t *testing.T) {
	t.Parallel()
	res, err := runGraphOnce(t, &react.RulePlanner{}, ledger.New().Tools(), react.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(res.Texts(), "\n")
	for _, want := range []string{"[1] thought: Check the runbooks", "obs [c1] search_internal_wiki", "obs [c2] get_merchant_payouts", "[2] thought:", "obs [c3] create_jira_ticket: LDG-1", "[3] thought:"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in the console output:\n%s", want, text)
		}
	}
}

func TestGraphStopsAtIterationLimit(t *testing.T) {
	t.Parallel()
	_, err := runGraphOnce(t, &endless{}, ledger.New().Tools(), react.Budget{MaxIterations: maxIterations})
	if !errors.Is(err, ErrMaxIterations) || !strings.Contains(err.Error(), "вичерпано ліміт 5 ітерацій") {
		t.Fatalf("err = %v; want errors.Is(err, ErrMaxIterations)", err)
	}
}

func TestGraphLoopDetected(t *testing.T) {
	t.Parallel()
	var calls int
	_, err := runGraphOnce(t, sameCall(), counted(&calls), react.Budget{MaxIterations: 5})
	if !errors.Is(err, ErrLoopDetected) || calls != 2 {
		t.Fatalf("err = %v, tool calls = %d", err, calls)
	}
}

func TestGraphDeadline(t *testing.T) {
	t.Parallel()
	slow := react.NewRegistry(react.Tool{Name: "search_internal_wiki", Call: func(context.Context, map[string]any) (string, error) {
		time.Sleep(15 * time.Millisecond)
		return "late", nil
	}})
	_, err := runGraphOnce(t, &endless{}, slow, react.Budget{MaxDuration: 20 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestGraphEmptyStepAndPlannerError(t *testing.T) {
	t.Parallel()
	if _, err := runGraphOnce(t, emptyStep(), ledger.New().Tools(), react.Budget{}); !errors.Is(err, ErrEmptyStep) {
		t.Errorf("empty step: err = %v", err)
	}
	boom := errors.New("provider down")
	p := &funcPlanner{fn: func(int, react.History) (react.Step, error) { return react.Step{}, boom }}
	if _, err := runGraphOnce(t, p, ledger.New().Tools(), react.Budget{}); err == nil || !strings.Contains(err.Error(), "provider down") {
		t.Errorf("planner error: err = %v", err)
	}
}

// The model turn (Step.Raw) is not in the event log, but it must still reach
// the next step's history: Gemini needs it replayed next to the results.
func TestGraphKeepsRawTurn(t *testing.T) {
	t.Parallel()
	raw := genai.NewContentFromText("model turn", genai.RoleModel)
	var seen *genai.Content
	p := &funcPlanner{fn: func(n int, h react.History) (react.Step, error) {
		if n == 1 {
			return react.Step{Raw: raw, Calls: []react.ToolCall{{ID: "c1", Name: "search_internal_wiki", Args: map[string]any{"query": "payout"}}}}, nil
		}
		seen = h[1].Raw
		if len(h[2].Results) != 1 || h[2].Results[0].CallID != "c1" {
			return react.Step{}, fmt.Errorf("no observation for c1 in history: %+v", h[2])
		}
		return react.Step{Final: true, Answer: "done"}, nil
	}}
	res, err := runGraphOnce(t, p, ledger.New().Tools(), react.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Final != "done" || seen != raw {
		t.Errorf("final = %q, raw kept = %v", res.Final, seen == raw)
	}
}
