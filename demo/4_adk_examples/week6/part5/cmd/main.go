// Week 6, part 5 — an agentic SDLC as a coordinator/worker dynamic workflow.
//
// A ticket comes in. A planner splits it into subtasks; the NUMBER of subtasks
// is not known until the planner has answered, which is exactly what a static
// graph cannot express and a dynamic node can. The coordinator then hands each
// subtask to a worker, runs a deterministic gate, and re-dispatches ONLY the
// worker whose output failed the gate — with a hard cap on re-dispatches.
//
//	go run .                  # offline: scripted planner and workers, no key
//	go run . -max-redispatch=0  # no second chances: the flaky worker stays red
//
// The pipeline stops at "ready for human review". It never merges. The last
// step of an agentic SDLC that matters is the one an agent is not allowed to
// take (week 5 day 10: approval is protocol state, not a courtesy).
//
// Note on concurrency: separate RunNode calls inside one dynamic node are
// sequential in ADK Go v2.6 (a RunNode is bound to its parent's sub-scheduler),
// so the workers here run one after another — deliberately, because each has
// its own gate and re-dispatch. For fan-out over a list from inside a dynamic
// node, dispatch ONE workflow.NewParallelWorker through RunNode; it runs the
// wrapped node concurrently per item (fanout_test.go proves it). Outside a
// dynamic node: a static AddFanOut + JoinNode graph, or a parallel agent (part3).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/internal/adkrun"
	"github.com/dimetron/ai-eng-course/labs/internal/scripted"
)

func main() { os.Exit(runCLI(os.Args[1:], os.Stdout)) }

// Subtask is one unit of work the planner hands the coordinator.
type Subtask struct {
	ID   string `json:"id"`
	Role string `json:"role"` // must be a known worker
	Goal string `json:"goal"`
}

// knownRoles is the coordinator's allowlist: the planner may ask for work, but
// it may not invent a worker.
var knownRoles = map[string]bool{"backend": true, "tests": true, "docs": true}

// Engine holds the run's configuration and audit trail; no package globals.
type Engine struct {
	Planner       model.LLM
	Workers       map[string]model.LLM
	MaxRedispatch int
	Log           []string
}

func (e *Engine) logf(format string, a ...any) { e.Log = append(e.Log, fmt.Sprintf(format, a...)) }

func runCLI(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("week6-part5", flag.ContinueOnError)
	maxRe := fs.Int("max-redispatch", 1, "how many times one worker may be re-run after failing the gate")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	e := &Engine{Planner: scriptedPlanner(), Workers: scriptedWorkers(), MaxRedispatch: *maxRe}
	report, err := e.Run(context.Background(), "TICKET-423: refunds must round half-up in integer cents")
	for _, l := range e.Log {
		fmt.Fprintln(out, "·", l)
	}
	if err != nil {
		fmt.Fprintln(out, "stopped:", err)
		return 1
	}
	fmt.Fprintln(out, report)
	return 0
}

// Run builds the one-node graph and executes it headlessly.
func (e *Engine) Run(ctx context.Context, ticket string) (string, error) {
	coordinator := workflow.NewDynamicNode[string, string]("coordinator",
		func(nc agent.Context, in string, _ func(*session.Event) error) (string, error) {
			return e.coordinate(nc, in)
		}, workflow.NodeConfig{})
	wf, err := adkrun.Pipeline("asdlc", "Coordinator/worker agentic SDLC.", coordinator)
	if err != nil {
		return "", err
	}
	return adkrun.Run(ctx, wf, ticket, nil)
}

func (e *Engine) coordinate(nc agent.Context, ticket string) (string, error) {
	// 1. plan — the only place the number of workers is decided.
	planText, err := e.callAgent(nc, "planner", e.Planner, "Split the ticket into subtasks as a JSON array of {id,role,goal}.", ticket)
	if err != nil {
		return "", fmt.Errorf("planner: %w", err)
	}
	tasks, err := parsePlan(planText)
	if err != nil {
		return "", fmt.Errorf("planner output rejected: %w", err)
	}
	e.logf("plan: %d subtasks (%s)", len(tasks), roles(tasks))

	// 2. dispatch + deterministic gate, re-dispatching only what failed.
	done := map[string]string{}
	for _, t := range tasks {
		out, err := e.runWorker(nc, t)
		if err != nil {
			return "", err
		}
		done[t.ID] = out
	}
	// 3. the human gate — the coordinator reports; it does not merge.
	var b strings.Builder
	fmt.Fprintf(&b, "READY FOR HUMAN REVIEW (not merged): %d/%d subtasks green\n", len(done), len(tasks))
	for _, t := range tasks {
		fmt.Fprintf(&b, "  %s [%s] %s\n", t.ID, t.Role, done[t.ID])
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// runWorker runs one worker through the gate, re-dispatching on failure up to
// the cap. The gate is code: a worker result must start with "DONE".
func (e *Engine) runWorker(nc agent.Context, t Subtask) (string, error) {
	llm := e.Workers[t.Role]
	for attempt := 0; ; attempt++ {
		out, err := e.callAgent(nc, t.ID+"_"+t.Role, llm, "You are the "+t.Role+" worker. Reply DONE <summary> or FAIL <reason>.", t.Goal)
		if err != nil {
			return "", fmt.Errorf("%s: %w", t.ID, err)
		}
		if strings.HasPrefix(out, "DONE") {
			e.logf("%s [%s] attempt %d: green", t.ID, t.Role, attempt+1)
			return out, nil
		}
		e.logf("%s [%s] attempt %d: gate failed — %s", t.ID, t.Role, attempt+1, out)
		if attempt >= e.MaxRedispatch {
			return "", fmt.Errorf("%s [%s] still red after %d re-dispatch(es); stopping, nothing merged", t.ID, t.Role, e.MaxRedispatch)
		}
	}
}

// callAgent wraps one scripted model as an LlmAgent node and runs it as a child.
func (e *Engine) callAgent(nc agent.Context, name string, llm model.LLM, instruction, input string) (string, error) {
	a, err := llmagent.New(llmagent.Config{Name: name, Model: llm, Instruction: instruction})
	if err != nil {
		return "", err
	}
	node, err := workflow.NewAgentNode(a, workflow.NodeConfig{})
	if err != nil {
		return "", err
	}
	return workflow.RunNode[string](nc, node, input)
}

// parsePlan validates the planner's JSON: non-empty, known roles, unique ids.
func parsePlan(s string) ([]Subtask, error) {
	s = strings.TrimSpace(s)
	var tasks []Subtask
	if err := json.Unmarshal([]byte(s), &tasks); err != nil {
		return nil, fmt.Errorf("not a JSON array: %w", err)
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("empty plan")
	}
	seen := map[string]bool{}
	for _, t := range tasks {
		if !knownRoles[t.Role] {
			return nil, fmt.Errorf("unknown worker role %q", t.Role)
		}
		if t.ID == "" || seen[t.ID] {
			return nil, fmt.Errorf("missing or duplicate id %q", t.ID)
		}
		seen[t.ID] = true
	}
	return tasks, nil
}

func roles(ts []Subtask) string {
	var r []string
	for _, t := range ts {
		r = append(r, t.Role)
	}
	return strings.Join(r, ", ")
}

// scriptedPlanner and scriptedWorkers are test doubles: they prove the wiring,
// the gate and the re-dispatch rule — not the quality of any plan or patch.
func scriptedPlanner() model.LLM {
	plan := `[{"id":"T1","role":"backend","goal":"round half-up in integer cents"},` +
		`{"id":"T2","role":"tests","goal":"add the float64 regression case from bug 423"},` +
		`{"id":"T3","role":"docs","goal":"update the refund rounding note"}]`
	return scripted.New("planner", scripted.Say(plan, 60))
}

func scriptedWorkers() map[string]model.LLM {
	return map[string]model.LLM{
		"backend": scripted.New("backend", scripted.Say("DONE Refund uses int64 cents", 40)),
		// the tests worker is flaky once: fails the gate, then succeeds when re-dispatched
		"tests": scripted.New("tests", scripted.Say("FAIL test does not compile: undefined refundCents", 40), scripted.Say("DONE added TestRefundHalfUpLargeAmount", 40)),
		"docs":  scripted.New("docs", scripted.Say("DONE note updated", 20)),
	}
}
