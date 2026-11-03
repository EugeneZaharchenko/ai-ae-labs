package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The tests come in two contours, and the split is deliberate.
//
// Most of them drive h.open directly, on a plain context, with no runner and no
// model. That is why open takes a context.Context while OpenRefundCase takes an
// agent.Context: the decision table is ordinary logic, and ordinary logic is tested
// without a framework in the room. The last two are end-to-end through a real ADK
// run with the offline stand-in model, and they exist to pin the one thing the unit
// tests cannot see — that the shape ADK hands the model matches the shape the code
// assumes.

// harnessFor builds a harness over a case store and a scripted fault plan.
func harnessFor(faults ...Fault) (*Harness, *MemCases, *FaultPlan) {
	cases := &MemCases{}
	plan := &FaultPlan{}
	plan.Push(faults...)
	return &Harness{Desk: &Desk{Cases: cases, Faults: plan}, MaxAttempts: 4}, cases, plan
}

// wellFormed is a request the desk accepts, so a test that means to exercise a
// fault is not accidentally measuring the validator.
func wellFormed() RefundCaseInput {
	return RefundCaseInput{TransactionID: defaultTxn, MerchantID: defaultMerchant}
}

func TestClassifyError(t *testing.T) {
	// The classifier is the whole decision table, so it is tested as one. Two rows
	// matter more than the rest: a %w-wrapped failure classifies correctly and a
	// %v-formatted one does not, and the second is a real bug rather than a
	// curiosity — it is how a transient failure becomes a fatal one in production
	// without anybody changing the error type.
	tests := []struct {
		name string
		err  error
		want Class
	}{
		{"transient sentinel", ErrTransient, ClassRetryTransient},
		{"deadline exceeded", context.DeadlineExceeded, ClassRetryTransient},
		{"wrapped transient", fmt.Errorf("open_refund_case: %w", ErrTransient), ClassRetryTransient},
		{"typed transient", &TransientError{Op: "open", Reason: "503"}, ClassRetryTransient},
		{"semantic field error", &SemanticError{Field: "merchant_id", Reason: "is empty"}, ClassSurfaceToModel},
		{"bare semantic sentinel", ErrSemantic, ClassSurfaceToModel},
		{"wrapped semantic", fmt.Errorf("validate: %w", &SemanticError{Field: "merchant_id"}), ClassSurfaceToModel},
		{"cancelled run", context.Canceled, ClassFatal},
		{"unclassified", errors.New("acquirer returned a body we could not parse"), ClassFatal},
		{"mangled by %v", fmt.Errorf("tool: %v", ErrTransient), ClassFatal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyError(tt.err); got != tt.want {
				t.Errorf("classifyError(%v) = %s, want %s", tt.err, got, tt.want)
			}
		})
	}
}

func TestHarnessRecoversFromTransientFailures(t *testing.T) {
	h, cases, plan := harnessFor(Unavailable, Timeout)
	h.Backoff = 0

	got, err := h.open(context.Background(), wellFormed())
	if err != nil {
		t.Fatalf("a call that eventually succeeded returned an error: %v", err)
	}
	if got.Status != "pending" || got.CaseID == "" {
		t.Errorf("want an open case, got %+v", got)
	}
	if cases.Count() != 1 {
		t.Errorf("want exactly one case opened, got %d", cases.Count())
	}
	if plan.Len() != 0 {
		t.Errorf("two queued faults went unused: %d left", plan.Len())
	}

	m := h.Metrics()
	// Three attempts reached the acquirer: two failed, the third did not. The
	// recovery rate is the fraction of CALLS that came back, which is why it counts
	// recovered calls rather than attempts.
	if m.Calls != 1 || m.Attempts != 3 || m.Transients != 2 || m.Recovered != 1 {
		t.Errorf("counters: %+v", m)
	}
	if m.RetriesToSuccess != 2 || m.GivenUp != 0 {
		t.Errorf("the retries spent on the successful call are the cost of recovery: %+v", m)
	}
	line := h.Line()
	if !strings.Contains(line, "recovery 100% (1 of 1)") {
		t.Errorf("report does not state the recovery rate: %q", line)
	}
	if !strings.Contains(line, "retries-to-success 2") {
		t.Errorf("report does not state what recovery cost: %q", line)
	}
	if strings.Contains(line, "HALTED") {
		t.Errorf("a recovered run is not a halted one: %q", line)
	}
}

func TestHarnessGivesUpAfterMaxAttempts(t *testing.T) {
	h, cases, plan := harnessFor(Unavailable, Unavailable, Unavailable, Unavailable)
	h.MaxAttempts = 4
	h.Backoff = 0

	got, err := h.open(context.Background(), wellFormed())
	// Giving up returns nil, and that is the part worth reading twice. The model is
	// told the acquirer is down through an observation it can act on; an error here
	// would reach ADK, which would convert it into an observation of its own and
	// lose the "do not call again" that bounds the run.
	if err != nil {
		t.Fatalf("giving up is an outcome, not an error: %v", err)
	}
	if got.Status != "unavailable" {
		t.Errorf("want unavailable, got %+v", got)
	}
	if !strings.Contains(got.Observation, "Do not call this tool again") {
		t.Errorf("the closing observation has to stop the model, not invite another round: %q", got.Observation)
	}
	if cases.Count() != 0 {
		t.Errorf("no case may be opened when the acquirer never answered: %d", cases.Count())
	}
	if plan.Len() != 0 {
		t.Errorf("the retries did not consume the queued faults: %d left", plan.Len())
	}

	m := h.Metrics()
	if m.Attempts != 4 || m.GivenUp != 1 || m.Recovered != 0 || m.Transients != 4 {
		t.Errorf("counters: %+v", m)
	}
	if !strings.Contains(h.Line(), "recovery 0% (0 of 1)") {
		t.Errorf("report: %q", h.Line())
	}
}

func TestHarnessSurfacesASemanticErrorWithoutConsumingAFault(t *testing.T) {
	h, _, plan := harnessFor(Unavailable)

	got, err := h.open(context.Background(), RefundCaseInput{
		TransactionID: defaultTxn,
		MerchantID:    "undefined", // the 03:00 incident, in one line
	})
	if err != nil {
		t.Fatalf("a request only the model can fix is not a harness error: %v", err)
	}
	if got.Status != "invalid_argument" || got.Field != "merchant_id" {
		t.Errorf("the model needs the field name, not just the verdict: %+v", got)
	}
	if !strings.Contains(got.Observation, "merchant_id") {
		t.Errorf("the observation has to name the field too: %q", got.Observation)
	}

	// Validation runs before the fault plan, so a malformed request neither costs a
	// network round trip nor burns one of the run's scripted outages. Both halves of
	// that are asserted, because they fail independently.
	if plan.Len() != 1 {
		t.Errorf("a rejected request consumed a queued fault: %d left", plan.Len())
	}
	m := h.Metrics()
	if m.Attempts != 0 {
		t.Errorf("a request the validator rejected never reached the acquirer: %+v", m)
	}
	if m.Calls != 1 || m.SelfCorrections != 1 {
		t.Errorf("counters: %+v", m)
	}
}

func TestHarnessStopsAtTheSelfCorrectionLimit(t *testing.T) {
	h, _, plan := harnessFor(Unavailable)
	bad := RefundCaseInput{TransactionID: defaultTxn, MerchantID: "undefined"}

	// Three corrections, then a stop. Without the ceiling this is an infinite loop
	// in which the model asks, is refused, and asks again — the shape of every
	// runaway agent, and the reason the budget is a constant rather than a config.
	for i := 1; i <= maxSelfCorrections; i++ {
		got, err := h.open(context.Background(), bad)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if got.Status != "invalid_argument" {
			t.Fatalf("call %d: want invalid_argument, got %+v", i, got)
		}
	}

	got, err := h.open(context.Background(), bad)
	if err != nil {
		t.Fatalf("the exhausted budget is an outcome, not an error: %v", err)
	}
	if got.Status != "stopped" {
		t.Errorf("the fourth rejected request must stop the run, got %+v", got)
	}
	if !strings.Contains(got.Observation, "Do not call this tool again") {
		t.Errorf("past the limit the observation is an instruction, not advice: %q", got.Observation)
	}
	if plan.Len() != 1 {
		t.Errorf("none of the rejected requests should have touched the acquirer: %d faults left", plan.Len())
	}
	if m := h.Metrics(); m.Transients != 0 {
		t.Errorf("a semantic failure is not a transient one: %+v", m)
	}
}

func TestHarnessFatalErrorIsRecordedNotSwallowed(t *testing.T) {
	h, cases, plan := harnessFor(Corrupt)

	got, err := h.open(context.Background(), wellFormed())
	// The error travels out, but the interesting assertion is the next one: ADK
	// discards this RefundResult and hands the model an observation of its own, so
	// returning an error is not by itself enough to fail the run. The marker is.
	if err == nil {
		t.Fatal("an unclassified failure must not be absorbed")
	}
	if h.Halted() == nil {
		t.Error("the harness did not record the fatal error")
	}
	if !strings.Contains(h.Line(), "HALTED") {
		t.Errorf("a halted run must say so in the report: %q", h.Line())
	}
	m := h.Metrics()
	if m.Attempts != 1 || m.Transients != 0 || m.GivenUp != 0 {
		t.Errorf("a fatal error is retried zero times and is not a transient: %+v", m)
	}
	if cases.Count() != 0 || plan.Len() != 0 {
		t.Errorf("the single attempt consumed the one fault and opened nothing: cases=%d faults=%d",
			cases.Count(), plan.Len())
	}
	_ = got
}

func TestLineNamesAZeroRatherThanReportingARecoveryRate(t *testing.T) {
	h, _, _ := harnessFor()
	h.Backoff = 0

	if _, err := h.open(context.Background(), wellFormed()); err != nil {
		t.Fatalf("open: %v", err)
	}
	line := h.Line()
	// "recovery 0%" and "nothing failed" are different facts and only one is bad
	// news. A line that computed the rate anyway would divide by zero, or worse,
	// report 0% for a call that never had anything to recover from.
	if !strings.Contains(line, "recovery n/a (nothing failed)") {
		t.Errorf("want the no-failure case named: %q", line)
	}
	if strings.Contains(line, "%") {
		t.Errorf("no percentage belongs on a run where nothing failed: %q", line)
	}
}

func TestWaitGivesUpWhenTheRunIsCancelled(t *testing.T) {
	// A sleep that outlives its context turns a shutdown into a hang. The backoff is
	// an hour here so that a passing test cannot be explained by the timer firing.
	h := &Harness{Backoff: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := h.wait(ctx, 1, &TransientError{Op: "open", Reason: "503"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want the context's error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the wait outlived its context: %s", elapsed)
	}
}

func TestWaitHonoursRetryAfter(t *testing.T) {
	// The dependency's Retry-After REPLACES our backoff rather than being merged
	// with it. With a base backoff of an hour, honouring means returning in
	// milliseconds — and a harness that took the maximum instead would sleep an
	// hour here, which is why the base is absurd rather than realistic.
	h := &Harness{Backoff: time.Hour}

	start := time.Now()
	if err := h.wait(context.Background(), 1, &TransientError{Op: "open", Reason: "429", RetryAfter: 5 * time.Millisecond}); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Retry-After did not replace the backoff: waited %s", elapsed)
	}
}

// The two end-to-end runs. `go test ./...` runs them offline: the stand-in model
// needs no provider, no key and no network, so the whole decision table is
// exercised by an ordinary `go test`, with no build tag and no LAB_LIVE-style
// opt-in. The live path is the CLI itself; it is what the learner runs.
//
// What these pin that the unit tests cannot: the offline model reads
// reply["status"] at the TOP LEVEL of the FunctionResponse, so the run only
// completes if ADK really does hand the model a flat map of RefundResult's json
// fields rather than wrapping it under "result". A wire-shape change would show up
// here as a stand-in that answers "something this stand-in does not know how to
// handle", and the assertions below name the case id, which only comes back from a
// reply that was read correctly.

func TestCLIOfflineRetriesThenOpensTheCase(t *testing.T) {
	var out bytes.Buffer
	code := runCLI([]string{"-offline", "-faults", "503,timeout"}, &out)
	report := out.String()

	if code != 0 {
		t.Fatalf("exit %d\n%s", code, report)
	}
	// The case id is printed by the stand-in model, which can only have read it from
	// the tool's reply — so its presence is the wire-shape assertion.
	if !strings.Contains(report, "rc-"+defaultTxn+"-"+defaultMerchant) {
		t.Errorf("the run did not reach the case it opened:\n%s", report)
	}
	if !strings.Contains(report, "refund cases opened: 1") {
		t.Errorf("the case store, not the prose, is the evidence:\n%s", report)
	}
	if !strings.Contains(report, "recovery 100% (1 of 1)") {
		t.Errorf("two injected failures were not recovered:\n%s", report)
	}
	if !strings.Contains(report, "retries-to-success 2") {
		t.Errorf("the report does not state that recovery took two attempts:\n%s", report)
	}
	if !strings.Contains(report, "self-corrections 1") {
		t.Errorf("the model should have corrected its placeholder arguments once:\n%s", report)
	}
	// A run that leaves faults queued did not exercise what the operator asked for.
	if !strings.Contains(report, "faults left in the plan: 0") {
		t.Errorf("an injected fault went unspent:\n%s", report)
	}
	if strings.Contains(report, "halted:") {
		t.Errorf("a recovered run is not halted:\n%s", report)
	}
	// The offline stand-in reports no usage. Printing "0 in + 0 out" next to a price
	// would be a fabricated bill, so the line has to say what the zero means.
	if !strings.Contains(report, "no usage reported") {
		t.Errorf("the offline run must not present token counts it does not have:\n%s", report)
	}
}

func TestCLIOfflineHaltsOnAnUnclassifiedFailure(t *testing.T) {
	var out bytes.Buffer
	code := runCLI([]string{"-offline", "-faults", "corrupt"}, &out)
	report := out.String()

	// This is the assertion the whole exit-code design exists for: the run itself
	// completed, the model answered politely, and only the harness's marker turns
	// that into a failure a shell can see.
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, report)
	}
	if !strings.Contains(report, "halted: acquirer returned a body we could not parse") {
		t.Errorf("the fatal error is not named in the report:\n%s", report)
	}
	if !strings.Contains(report, "refund cases opened: 0 — the run was halted") {
		t.Errorf("the zero has to name its cause:\n%s", report)
	}
}

func TestCLIRejectsAnUnknownFaultBeforeResolvingAModel(t *testing.T) {
	// The order matters: a typo in the command line is answered without probing for
	// a provider. A CLI that resolves the model first makes a two-character mistake
	// cost a network round trip.
	var out bytes.Buffer
	if code := runCLI([]string{"-faults", "nope"}, &out); code != 2 {
		t.Errorf("want exit 2 for a bad flag value, got %d", code)
	}
	if out.Len() != 0 {
		t.Errorf("nothing belongs on stdout when the command line is wrong: %q", out.String())
	}
}
