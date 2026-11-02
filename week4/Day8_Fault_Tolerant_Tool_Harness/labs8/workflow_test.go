package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/contrib/googleadk"
	"go.temporal.io/sdk/converter"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/agentdb"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory/graphmemorytest"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/ledger"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/llm"
)

// These tests need no API key, no Docker and no Temporal server: the workflow
// runs in testsuite, the model is scripted, faults come from ledger.FaultPlan,
// SQLite is a temp file and memory is the in-process graphmemorytest.Fake.

const refundMsg = "Open a refund for txn-2026-07-118845 at merchant A-114"

// world is what survives between turns: the SQLite file and the memory store.
type world struct {
	db     *agentdb.DB
	faults *ledger.FaultPlan
	mem    *graphmemory.Service
}

func newWorld(t *testing.T, faults ...ledger.Fault) *world {
	t.Helper()
	db, err := agentdb.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	plan := &ledger.FaultPlan{}
	plan.Push(faults...)
	return &world{db: db, faults: plan, mem: graphmemory.New(&graphmemorytest.Fake{})}
}

type testRegistry struct {
	*testsuite.TestWorkflowEnvironment
}

func (testRegistry) RegisterNexusService(*nexus.Service) {}

// turn is the outcome of one workflow run.
type turn struct {
	res    Result
	err    error
	starts map[string]int // Activity name → how many attempts were started
}

func (tr turn) log() string { return strings.Join(tr.res.Events, "\n") }

// runTurn executes AgentWorkflow once against w with model m.
func runTurn(t *testing.T, w *world, m model.LLM, sessionID, msg string) turn {
	t.Helper()
	var s testsuite.WorkflowTestSuite
	s.SetLogger(tlog.NewStructuredLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(AgentWorkflow)
	env.RegisterActivity(&Activities{
		Desk:     &ledger.Desk{Cases: w.db.Cases(), Faults: w.faults},
		Sessions: w.db.Sessions(),
		Memory:   w.mem,
	})
	acts, err := googleadk.NewActivities(adkConfig(m, w.db))
	if err != nil {
		t.Fatal(err)
	}
	acts.Register(testRegistry{env})

	var mu sync.Mutex
	starts := map[string]int{}
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		mu.Lock()
		defer mu.Unlock()
		starts[info.ActivityType.Name]++
	})

	env.ExecuteWorkflow(AgentWorkflow, Request{UserID: "taras", SessionID: sessionID, Message: msg})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	tr := turn{err: env.GetWorkflowError(), starts: starts}
	if tr.err == nil {
		if err := env.GetWorkflowResult(&tr.res); err != nil {
			t.Fatal(err)
		}
	}
	return tr
}

func (w *world) counts(t *testing.T) (calls, cases int64) {
	t.Helper()
	ctx := context.Background()
	calls, err := w.db.CountLLMCalls(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cases, err = w.db.CountCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return calls, cases
}

// Scenario 1: the acquirer answers 503 twice. Temporal retries the Activity,
// the third attempt passes, the model never learns about the failure.
func TestTransientFailuresAreRetriedByTheEngine(t *testing.T) {
	t.Parallel()
	w := newWorld(t, ledger.Unavailable, ledger.Unavailable)
	tr := runTurn(t, w, llm.Demo{}, "s1", refundMsg)
	if tr.err != nil {
		t.Fatal(tr.err)
	}
	if n := tr.starts["OpenRefundCase"]; n != 3 {
		t.Errorf("OpenRefundCase attempts = %d, want 3 (2 × 503 + success)", n)
	}
	if strings.Contains(tr.log(), "ERROR") {
		t.Errorf("the model saw a transient failure:\n%s", tr.log())
	}
	if calls, cases := w.counts(t); calls != 2 || cases != 1 {
		t.Errorf("llm_calls=%d refund_cases=%d, want 2 and 1", calls, cases)
	}
}

// Scenario 2: network timeout. Same class, same owner: the engine.
func TestNetworkTimeoutIsRetried(t *testing.T) {
	t.Parallel()
	w := newWorld(t, ledger.Timeout, ledger.Timeout, ledger.Timeout)
	tr := runTurn(t, w, llm.Demo{}, "s1", refundMsg)
	if tr.err != nil {
		t.Fatal(tr.err)
	}
	if n := tr.starts["OpenRefundCase"]; n != 4 {
		t.Errorf("OpenRefundCase attempts = %d, want 4", n)
	}
	if !strings.Contains(tr.res.Answer, "rc-txn-2026-07-118845-A-114") {
		t.Errorf("answer = %q", tr.res.Answer)
	}
}

// Scenario 3: invalid arguments. merchant_id="undefined" is not retried even
// once: it goes straight back to the model, which fixes the call.
func TestSemanticErrorBecomesObservationAndSelfCorrects(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	tr := runTurn(t, w, llm.Demo{Sloppy: true}, "s1", refundMsg)
	if tr.err != nil {
		t.Fatal(tr.err)
	}
	if n := tr.starts["OpenRefundCase"]; n != 2 {
		t.Errorf("OpenRefundCase attempts = %d, want 2 (undefined once, then fixed)", n)
	}
	if tr.res.SelfCorrections != 1 {
		t.Errorf("self corrections = %d", tr.res.SelfCorrections)
	}
	// The order Taras reads in the log.
	want := []string{
		"[ev:tool_call] open_refund_case map[merchant_id:undefined",
		"[ev:tool_observation ERROR] open_refund_case map[attempt:1 field:merchant_id",
		"[ev:tool_call] open_refund_case map[merchant_id:A-114",
		"[ev:tool_observation] open_refund_case map[case_id:rc-txn-2026-07-118845-A-114",
		"[ev:answer] Refund case rc-txn-2026-07-118845-A-114 is open",
	}
	i := 0
	for _, line := range tr.res.Events {
		if i < len(want) && strings.HasPrefix(line, want[i]) {
			i++
		}
	}
	if i != len(want) {
		t.Errorf("log does not follow call → ERROR → fixed call → success (matched %d of %d):\n%s", i, len(want), tr.log())
	}
	if _, cases := w.counts(t); cases != 1 {
		t.Errorf("refund_cases = %d", cases)
	}
}

// Scenario 4: the dependency stays down. After MaximumAttempts the engine
// gives up and the model gets status "unavailable" — not a crash, not a loop.
func TestDependencyUnavailable(t *testing.T) {
	t.Parallel()
	w := newWorld(t, ledger.Unavailable, ledger.Unavailable, ledger.Unavailable, ledger.Unavailable)
	tr := runTurn(t, w, llm.Demo{}, "s1", refundMsg)
	if tr.err != nil {
		t.Fatal(tr.err)
	}
	if n := tr.starts["OpenRefundCase"]; n != int(toolRetry.MaximumAttempts) {
		t.Errorf("OpenRefundCase attempts = %d, want %d", n, toolRetry.MaximumAttempts)
	}
	if !strings.Contains(tr.log(), "[ev:tool_observation ERROR] open_refund_case map[observation:ERROR: open_refund_case: acquirer returned 503") ||
		!strings.Contains(tr.log(), "status:unavailable") {
		t.Errorf("no unavailable observation:\n%s", tr.log())
	}
	if !strings.Contains(tr.res.Answer, "could not open") {
		t.Errorf("answer = %q", tr.res.Answer)
	}
	if _, cases := w.counts(t); cases != 0 {
		t.Errorf("refund_cases = %d, want 0", cases)
	}
}

// A model that never fixes its arguments: the 4th semantic error ends the turn
// with an explicit SelfCorrectionExhausted error, not silently.
func TestSelfCorrectionBudgetExhausted(t *testing.T) {
	t.Parallel()
	var script []*model.LLMResponse
	for i := 1; i <= maxSelfCorrections+2; i++ {
		script = append(script, googleadk.FunctionCallResponse(fmt.Sprintf("c%d", i), "open_refund_case",
			map[string]any{"transaction_id": "txn-2026-07-118845", "merchant_id": "undefined"}))
	}
	w := newWorld(t)
	tr := runTurn(t, w, googleadk.NewFakeModel(script...), "s1", refundMsg)
	var appErr *temporal.ApplicationError
	if !errors.As(tr.err, &appErr) || appErr.Type() != errTypeExhausted {
		t.Fatalf("err = %v, want ApplicationError %s", tr.err, errTypeExhausted)
	}
	if !strings.Contains(appErr.Message(), ErrSelfCorrectionExhausted.Error()) {
		t.Errorf("message = %q", appErr.Message())
	}
	if n := tr.starts["OpenRefundCase"]; n != maxSelfCorrections+1 {
		t.Errorf("OpenRefundCase attempts = %d, want %d", n, maxSelfCorrections+1)
	}
	if n := tr.starts[googleadk.InvokeModelActivityName]; n != maxSelfCorrections+1 {
		t.Errorf("model calls = %d: the turn must stop right after the 4th error", n)
	}
}

// A second call for the same transaction and merchant returns replayed=true
// instead of a second case.
func TestRefundCaseIsIdempotent(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	first := runTurn(t, w, llm.Demo{}, "s1", refundMsg)
	second := runTurn(t, w, llm.Demo{}, "s2", refundMsg)
	if first.err != nil || second.err != nil {
		t.Fatal(first.err, second.err)
	}
	if !strings.Contains(second.log(), "replayed:true") {
		t.Errorf("second turn did not replay:\n%s", second.log())
	}
	if _, cases := w.counts(t); cases != 1 {
		t.Errorf("refund_cases = %d, want 1", cases)
	}
}

// L3: the same session continues from SQLite, and a NEW session recalls A-114
// from memory written at the end of the earlier one.
func TestSessionContinuesAndMemoryRecalls(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	first := runTurn(t, w, llm.Demo{}, "s1", refundMsg)
	if first.err != nil {
		t.Fatal(first.err)
	}
	next := runTurn(t, w, llm.Demo{}, "s1", "Show the payouts of merchant A-114")
	if next.err != nil {
		t.Fatal(next.err)
	}
	if next.res.LoadedEvents < 4 {
		t.Errorf("second turn of s1 loaded %d events, want the whole first turn", next.res.LoadedEvents)
	}
	if !strings.Contains(next.res.Answer, "batch #418") {
		t.Errorf("payouts answer = %q", next.res.Answer)
	}

	recall := runTurn(t, w, llm.Demo{}, "s9", "What do you remember about merchant A-114?")
	if recall.err != nil {
		t.Fatal(recall.err)
	}
	if recall.res.LoadedEvents != 0 {
		t.Errorf("s9 is a new session, loaded %d events", recall.res.LoadedEvents)
	}
	if recall.starts["OpenNodes"] != 1 || !strings.Contains(recall.res.Answer, "rc-txn-2026-07-118845-A-114") {
		t.Errorf("recall did not find the earlier case:\n%s", recall.log())
	}
}

func TestSearchNodesTool(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if tr := runTurn(t, w, llm.Demo{}, "s1", refundMsg); tr.err != nil {
		t.Fatal(tr.err)
	}
	m := googleadk.NewFakeModel(
		googleadk.FunctionCallResponse("c1", "search_nodes", map[string]any{"query": "refund"}),
		googleadk.TextResponse("found it"),
	)
	tr := runTurn(t, w, m, "s2", "anything about refunds?")
	if tr.err != nil || tr.starts["SearchNodes"] != 1 || !strings.Contains(tr.log(), "A-114") {
		t.Fatalf("err = %v, starts = %v\n%s", tr.err, tr.starts, tr.log())
	}
}

// failingStore breaks the memory scope: a bug on our side, so FatalError.
type failingStore struct{}

func (failingStore) Upsert(context.Context, []graphmemory.Turn) error { return nil }
func (failingStore) Search(context.Context, graphmemory.Query) ([]graphmemory.Turn, error) {
	return nil, fmt.Errorf("store: %w", graphmemory.ErrScope)
}

// A fatal error is neither retried nor shown to the model: the turn stops.
func TestFatalErrorStopsTheTurn(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.mem = graphmemory.New(failingStore{})
	tr := runTurn(t, w, llm.Demo{}, "s1", "What do you remember about A-114?")
	var appErr *temporal.ApplicationError
	if !errors.As(tr.err, &appErr) || appErr.Type() != errTypeFatal {
		t.Fatalf("err = %v, want %s", tr.err, errTypeFatal)
	}
	if n := tr.starts["OpenNodes"]; n != 1 {
		t.Errorf("OpenNodes attempts = %d, want 1 (fatal is not retried)", n)
	}
}
