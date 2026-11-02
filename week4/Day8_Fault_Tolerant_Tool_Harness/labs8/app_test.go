package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/mocks"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/ledger"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/llm"
)

func TestParseFaults(t *testing.T) {
	t.Parallel()
	plan, err := parseFaults("503, timeout,429,")
	if err != nil || plan.Len() != 3 {
		t.Fatalf("plan len = %d, err = %v", plan.Len(), err)
	}
	if f := plan.Next(); f != ledger.Unavailable {
		t.Errorf("first fault = %v", f)
	}
	if _, err := parseFaults("500"); err == nil {
		t.Error("unknown fault must be an error")
	}
}

func TestParseLatency(t *testing.T) {
	t.Parallel()
	if d, err := parseLatency("15s"); err != nil || d != 15*time.Second {
		t.Errorf("15s → %v, %v", d, err)
	}
	if d, err := parseLatency(""); err != nil || d != 0 {
		t.Errorf("empty → %v, %v", d, err)
	}
	if _, err := parseLatency("soon"); err == nil {
		t.Error("bad duration must be an error")
	}
}

func TestNewDeskFromEnv(t *testing.T) {
	t.Setenv("LEDGER_FAULTS", "503,503")
	t.Setenv("LEDGER_LATENCY", "2s")
	d, err := newDesk(&ledger.MemCases{})
	if err != nil || d.Faults.Len() != 2 || d.Latency != 2*time.Second {
		t.Fatalf("desk = %+v, err = %v", d, err)
	}
	t.Setenv("LEDGER_LATENCY", "x")
	if _, err := newDesk(&ledger.MemCases{}); err == nil {
		t.Error("bad latency must fail")
	}
	t.Setenv("LEDGER_FAULTS", "boom")
	if _, err := newDesk(&ledger.MemCases{}); err == nil {
		t.Error("bad faults must fail")
	}
}

type countRecorder struct{ n int }

func (c *countRecorder) RecordLLMCall(context.Context, string) error { c.n++; return nil }

func TestAdkConfigCountsEveryCall(t *testing.T) {
	t.Parallel()
	rec := &countRecorder{}
	m, err := adkConfig(llm.Demo{}, rec).Models[modelName](context.Background(), modelName)
	if err != nil {
		t.Fatal(err)
	}
	for range m.GenerateContent(context.Background(), &model.LLMRequest{}, false) {
	}
	if rec.n != 1 || m.Name() != "demo-rules" {
		t.Errorf("calls = %d, name = %s", rec.n, m.Name())
	}
}

func TestPickModelDemo(t *testing.T) {
	t.Setenv("LABS8_MODEL", "demo")
	m, which, err := pickModel(context.Background())
	if err != nil || m.Name() != "demo-rules" || !strings.Contains(which, "offline") {
		t.Fatalf("m = %v, which = %q, err = %v", m, which, err)
	}
}

func TestRunCommands(t *testing.T) {
	t.Setenv("AGENT_DB", filepath.Join(t.TempDir(), "agent.db"))
	t.Setenv("TEMPORAL_ADDRESS", "127.0.0.1:1") // nothing listens there
	t.Setenv("LABS8_MODEL", "demo")
	ctx := context.Background()
	var out bytes.Buffer

	if err := run(ctx, []string{"counter"}, &out); err != nil || out.String() != "llm_calls=0 refund_cases=0 tokens_in=0 tokens_out=0\n" {
		t.Errorf("counter: %q, %v", out.String(), err)
	}
	if err := run(ctx, nil, &out); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("no args: %v", err)
	}
	if err := run(ctx, []string{"fly"}, &out); err == nil {
		t.Error("unknown command must fail")
	}
	if err := run(ctx, []string{"start", "-session", "s1"}, &out); err == nil {
		t.Error("start without a message must fail")
	}
	if err := run(ctx, []string{"start", "-nope"}, &out); err == nil {
		t.Error("bad flag must fail")
	}
	if err := run(ctx, []string{"start", "hello"}, &out); err == nil || !strings.Contains(err.Error(), "temporal server start-dev") {
		t.Errorf("start without Temporal: %v", err)
	}
	// The worker stops on the first missing dependency and says which one.
	t.Setenv("MEMORY_BACKEND", "neo4j")
	if err := run(ctx, []string{"worker"}, &out); err == nil || !strings.Contains(err.Error(), "neo4j") {
		t.Errorf("worker with a non-built backend: %v", err)
	}
	t.Setenv("LEDGER_FAULTS", "boom")
	if err := run(ctx, []string{"worker"}, &out); err == nil {
		t.Error("worker with bad LEDGER_FAULTS must fail")
	}
}

func TestPrintResult(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	printResult(&out, Result{Events: []string{"[ev:answer] ok"}, SelfCorrections: 1, LoadedEvents: 4}, nil)
	for _, want := range []string{"continued: 4 events", "[ev:answer] ok", "[ev:success] self_corrections=1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in %q", want, out.String())
		}
	}
	out.Reset()
	printResult(&out, Result{}, temporal.NewApplicationError("4 semantic errors", errTypeExhausted))
	if !strings.Contains(out.String(), "[ev:failed] SelfCorrectionExhausted: 4 semantic errors") {
		t.Errorf("got %q", out.String())
	}
	out.Reset()
	printResult(&out, Result{}, errors.New("plain"))
	if out.String() != "[ev:failed] plain\n" {
		t.Errorf("got %q", out.String())
	}
}

// fakeRun stands for the WorkflowRun of a real server: Get runs the workflow
// in the test environment, so `start` is tested end to end without Temporal.
type fakeRun struct {
	t   *testing.T
	w   *world
	req Request
}

func (r fakeRun) GetID() string                  { return "turn-test" }
func (r fakeRun) GetRunID() string               { return "run-test" }
func (r fakeRun) GetFirstExecutionRunID() string { return "run-test" }
func (r fakeRun) GetWithOptions(ctx context.Context, v any, _ client.WorkflowRunGetOptions) error {
	return r.Get(ctx, v)
}
func (r fakeRun) Get(_ context.Context, v any) error {
	tr := runTurn(r.t, r.w, llm.Demo{Sloppy: true}, r.req.SessionID, r.req.Message)
	*(v.(*Result)) = tr.res
	return tr.err
}

func TestStartPrintsTheEventLog(t *testing.T) {
	w := newWorld(t)
	c := mocks.NewClient(t)
	c.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(
		func(_ context.Context, opts client.StartWorkflowOptions, _ any, args ...any) (client.WorkflowRun, error) {
			if opts.TaskQueue != taskQueue {
				t.Errorf("task queue = %q", opts.TaskQueue)
			}
			return fakeRun{t: t, w: w, req: args[0].(Request)}, nil
		})
	c.On("Close").Return()
	orig := dialTemporal
	t.Cleanup(func() { dialTemporal = orig })
	dialTemporal = func() (client.Client, error) { return c, nil }

	var out bytes.Buffer
	if err := run(context.Background(), []string{"start", "-session", "s7", refundMsg}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"workflow turn-test (session s7)", "[ev:tool_observation ERROR]", "[ev:success] self_corrections=1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in:\n%s", want, out.String())
		}
	}
}

func TestPickModelWithoutProvider(t *testing.T) {
	for _, name := range []string{"LABS8_MODEL", "MODEL", "DEFAULT_MODEL_PROVIDER", "GOOGLE_API_KEY", "GEMINI_API_KEY",
		"OPENAI_API_KEY", "OLLAMA_BASE_URL", "OLLAMA_MODEL", "AGENTGATEWAY_BASE_URL", "AGENTGATEWAY_MODEL"} {
		t.Setenv(name, "")
	}
	t.Chdir(t.TempDir()) // no apps/.env above
	if _, _, err := pickModel(context.Background()); err == nil || !strings.Contains(err.Error(), "LABS8_MODEL=demo") {
		t.Errorf("err = %v, want a hint about LABS8_MODEL=demo", err)
	}
}

func TestStartAndCounterErrors(t *testing.T) {
	c := mocks.NewClient(t)
	c.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, errors.New("namespace default is not found"))
	c.On("Close").Return()
	orig := dialTemporal
	t.Cleanup(func() { dialTemporal = orig })
	dialTemporal = func() (client.Client, error) { return c, nil }
	var out bytes.Buffer
	if err := run(context.Background(), []string{"start", "hi"}, &out); err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Errorf("start: %v", err)
	}

	t.Setenv("LABS8_MODEL", "demo")
	t.Setenv("AGENT_DB", t.TempDir()) // a directory, not a file
	if err := run(context.Background(), []string{"counter"}, &out); err == nil {
		t.Error("counter on a broken db path must fail")
	}
	if err := run(context.Background(), []string{"worker"}, &out); err == nil {
		t.Error("worker on a broken db path must fail")
	}
}
