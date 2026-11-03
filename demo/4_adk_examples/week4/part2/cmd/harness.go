package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"google.golang.org/adk/v2/agent"
)

// The fault-tolerant tool harness.
//
// Week 4 part 1 wrote the loop by hand. Part 2 keeps the loop — ADK's llmagent
// runs it now — and puts a harness around the one tool that changes state. The
// split is the lesson: the framework owns the conversation, the harness owns the
// failure policy. Retrying, classifying, giving up and telling the model what
// happened are decisions a framework cannot make for you, because only you know
// which of your tools are safe to call twice.
//
// The decision table, which is the whole idea:
//
//	transient  → retry inside the harness, bounded, honouring Retry-After.
//	             The model never hears about it: a retry it cannot influence is
//	             noise in its context, and noise is what makes an agent loop drift.
//	semantic   → surface to the model as an observation naming the field, on a
//	             budget. Only the model can fix a wrong argument.
//	anything   → fatal. Record it, stop the run, tell a human. An unclassified
//	             error is not a transient one; treating it as either retries
//	             forever or lies to the model.
const ToolName = "open_refund_case"

// toolDescription is part of the harness too. A model cannot apply a policy it
// was never told, so the statuses are spelled out here rather than left for the
// model to infer from a failure.
const toolDescription = "Open a refund case with the acquirer for one transaction of one merchant. " +
	"Copy both ids from the user's request exactly as written; never invent one, and never pass a " +
	"placeholder such as \"undefined\". " +
	"The reply carries a status: pending means the case is open; invalid_argument means one field was " +
	"rejected and the observation names it — correct that field and call again; unavailable means the " +
	"acquirer is down and calling again will not help; stopped means you must stop."

const (
	// maxSelfCorrections is the budget for the model fixing its own arguments.
	// Three is not a magic number: it is enough for a real misreading and far short
	// of a loop, and the ceiling is what separates self-correction from spinning.
	maxSelfCorrections = 3

	// defaultMaxAttempts bounds the harness's own retries of one call. The lecture's
	// point about a recovery rate is that it is meaningless without this number
	// printed next to it.
	defaultMaxAttempts = 4
)

// ErrSelfCorrectionExhausted reports that the model burned its argument budget.
var ErrSelfCorrectionExhausted = errors.New("harness: self-correction budget exhausted")

// Class is what a failure means for the caller. The values map one-to-one onto
// the three branches above, and the names say what to DO rather than what went
// wrong — which is the difference between a classifier and a log level.
type Class int

const (
	// ClassRetryTransient — the same call may succeed shortly. Retry it here.
	ClassRetryTransient Class = iota + 1
	// ClassSurfaceToModel — the request itself is wrong. Only the model can fix it.
	ClassSurfaceToModel
	// ClassFatal — nobody knows what this is. Stop and get a human.
	ClassFatal
)

func (c Class) String() string {
	switch c {
	case ClassRetryTransient:
		return "transient"
	case ClassSurfaceToModel:
		return "semantic"
	default:
		return "fatal"
	}
}

// classifyError is the decision table as code. Order matters, and so does the
// default: an error nobody anticipated is fatal, not transient. A classifier that
// defaults to "retry" turns every novel failure into an outage that lasts as long
// as the retries do.
//
// The errors.Is calls walk wrapped chains, so a tool that wraps its failures with
// %w classifies correctly and one that formats with %v does not. That is not a
// subtlety to remember — it is a line in the tests, because it is a real bug.
func classifyError(err error) Class {
	var se *SemanticError
	switch {
	case errors.Is(err, context.Canceled):
		// A cancelled run is not a failure to retry. It is a decision somebody made.
		return ClassFatal
	case errors.As(err, &se):
		return ClassSurfaceToModel
	case errors.Is(err, ErrSemantic):
		return ClassSurfaceToModel
	case errors.Is(err, ErrTransient), errors.Is(err, context.DeadlineExceeded):
		return ClassRetryTransient
	default:
		return ClassFatal
	}
}

// RefundResult is what the model receives.
//
// ADK's functiontool converts this struct straight into a flat map of these json
// fields in the FunctionResponse, so the model reads reply["status"],
// reply["field"] and reply["observation"] at the top level. The observation is the
// only channel the harness has into the model's next decision, which is why every
// branch below fills it in.
type RefundResult struct {
	Status      string `json:"status"`
	CaseID      string `json:"case_id,omitempty"`
	Replayed    bool   `json:"replayed,omitempty"`
	Field       string `json:"field,omitempty"`
	Observation string `json:"observation,omitempty"`
	Attempt     int    `json:"attempt,omitempty"`
}

// Metrics is what the harness did, in numbers.
//
// Three of these are the lecture's three metrics: RecoveryRate, RetriesToSuccess
// and the time to diagnosis. The rest exist so that no count can be read without
// the one next to it — a recovery rate with no attempts behind it, or no ceiling
// on the retries, is a number that flatters.
type Metrics struct {
	Calls            int // times the model asked for the tool
	Attempts         int // times the acquirer was actually called
	Transients       int // failures the harness retried around
	Recovered        int // calls that eventually succeeded after at least one retry
	GivenUp          int // calls that exhausted the attempt budget
	SelfCorrections  int // semantic failures surfaced to the model
	RetriesToSuccess int // extra attempts spent on the calls that did recover

	firstFailure time.Time
	diagnosed    time.Time
}

// Harness owns the failure policy for one tool.
type Harness struct {
	Desk        *Desk
	Log         *slog.Logger
	MaxAttempts int           // 0 means defaultMaxAttempts
	Backoff     time.Duration // base delay; doubled per attempt. 0 means no delay.

	mu              sync.Mutex
	metrics         Metrics
	selfCorrections int
	halted          error
}

// OpenRefundCase is the tool handler.
//
// It takes an agent.Context because that is the signature ADK's functiontool
// wants; because an agent.Context embeds context.Context, it also carries the
// run's cancellation and deadline straight through to the acquirer call below.
func (h *Harness) OpenRefundCase(ctx agent.Context, in RefundCaseInput) (RefundResult, error) {
	return h.open(ctx, in)
}

// open is the harness proper, on a plain context so a test can drive it with no
// runner and no model at all. The interesting cases — a queued fault, a wrong
// field, a fatal — are all reachable here in a few lines.
func (h *Harness) open(ctx context.Context, in RefundCaseInput) (RefundResult, error) {
	h.count(func(m *Metrics) { m.Calls++ })

	// attempt counts calls that reached the acquirer. A request that validation
	// rejected never left the building, so it is not one of them — the same reason
	// it does not consume a queued fault.
	var attempt, retries int
	for {
		out, err := h.Desk.OpenRefundCase(ctx, in)
		if err == nil {
			attempt++
			h.count(func(m *Metrics) {
				m.Attempts++
				if retries > 0 {
					m.Recovered++
					m.RetriesToSuccess += retries
					m.diagnosed = time.Now()
				}
			})
			return RefundResult{Status: out.Status, CaseID: out.CaseID, Replayed: out.Replayed}, nil
		}

		switch classifyError(err) {
		case ClassSurfaceToModel:
			h.log(slog.LevelWarn, "tool rejected the arguments", err, attempt)
			return h.surface(err)

		case ClassRetryTransient:
			attempt++
			h.count(func(m *Metrics) {
				m.Attempts++
				m.Transients++
				if m.firstFailure.IsZero() {
					m.firstFailure = time.Now()
				}
			})
			retries++

			if attempt >= h.maxAttempts() {
				// The budget is spent. The model gets a closing observation that tells
				// it not to try, rather than an error that invites another round — the
				// difference between a harness that bounds the run and one that only
				// pretends to.
				h.count(func(m *Metrics) { m.GivenUp++ })
				h.log(slog.LevelError, "gave up on the acquirer", err, attempt)
				return RefundResult{
					Status: "unavailable",
					Observation: fmt.Sprintf("ERROR: the acquirer failed %d times in a row. "+
						"Tell the user the refund could not be opened right now and to try again later. "+
						"Do not call this tool again.", attempt),
					Attempt: attempt,
				}, nil
			}
			h.log(slog.LevelWarn, "tool failed, retrying", err, attempt)
			if werr := h.wait(ctx, attempt, err); werr != nil {
				return RefundResult{}, werr
			}

		default:
			// A fatal error is recorded and returned, and no attempt to retry it is
			// made. Note that ADK discards the RefundResult that travels with the
			// error and hands the model an observation of its own instead — see halt.
			attempt++
			h.count(func(m *Metrics) { m.Attempts++ })
			h.log(slog.LevelError, "fatal tool failure", err, attempt)
			h.halt(err)
			return RefundResult{
				Status:      "stopped",
				Observation: "ERROR: " + err.Error() + " — this is not a failure worth retrying. Stop and tell the user.",
			}, err
		}
	}
}

// surface hands a rejected request back to the model, on a budget.
//
// The message names the field, because "invalid argument" leaves the model with
// nothing to change and guarantees a second identical call. And the budget is
// what stops that from becoming a loop: past the limit the answer stops being
// advice and becomes an instruction to stop.
func (h *Harness) surface(err error) (RefundResult, error) {
	var se *SemanticError
	detail := err.Error()
	if errors.As(err, &se) {
		detail = se.Reason
	}

	h.mu.Lock()
	h.selfCorrections++
	h.metrics.SelfCorrections = h.selfCorrections
	used := h.selfCorrections
	h.mu.Unlock()

	if used > maxSelfCorrections {
		return RefundResult{
			Status: "stopped",
			Observation: fmt.Sprintf("STOP: the self-correction limit of %d is reached. "+
				"The arguments are still invalid. Do not call this tool again; tell the user the "+
				"request could not be completed.", maxSelfCorrections),
		}, nil
	}

	// A bare ErrSemantic carries no field name. Saying so is better than inventing
	// a field: a model told to fix "something" will change something at random.
	if se == nil {
		return RefundResult{
			Status: "invalid_argument",
			Observation: "ERROR: " + detail + " — the request was rejected as invalid, but the " +
				"failure did not name the field. Re-read the user's request and correct it.",
			Attempt: used,
		}, nil
	}
	return RefundResult{
		Status:      "invalid_argument",
		Field:       se.Field,
		Observation: fmt.Sprintf("ERROR: %s — correct %s from the user's request and call again.", detail, se.Field),
		Attempt:     used,
	}, nil
}

// wait pauses between attempts.
//
// Two things make this more than a sleep. A Retry-After from the dependency
// replaces our backoff rather than being merged with it, because the dependency is
// the authority on when it will be ready and retrying sooner is how a struggling
// service is kept down. And the wait is bound to the run's context, because a sleep
// that outlives its context turns a shutdown into a hang.
func (h *Harness) wait(ctx context.Context, attempt int, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	d := h.backoff(attempt)
	var te *TransientError
	if errors.As(err, &te) && te.RetryAfter > 0 {
		d = te.RetryAfter
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// backoff doubles per attempt. The attempt cap bounds the sum, so there is no cap
// here; if MaxAttempts ever grows past a handful this needs one.
func (h *Harness) backoff(attempt int) time.Duration {
	if h.Backoff <= 0 || attempt < 1 {
		return 0
	}
	return h.Backoff << (attempt - 1)
}

func (h *Harness) maxAttempts() int {
	if h.MaxAttempts > 0 {
		return h.MaxAttempts
	}
	return defaultMaxAttempts
}

// halt records the run's first fatal error.
//
// It is deliberately not surfaced to the model as an observation, because the
// model cannot act on it — and deliberately not swallowed, because a run that
// quietly continued past an unclassified failure is the failure mode this class
// exists to prevent. ADK cannot be made to abort a run from inside a tool: a
// handler's error becomes an observation the model reads (and a panic is recovered
// into an error before that), so the run carries on regardless. The abort is the
// caller's job, which is why this is a marker rather than an exception and why
// main checks it after the run and exits non-zero.
func (h *Harness) halt(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.halted == nil {
		h.halted = err
	}
}

// Halted reports the fatal error, if the run hit one.
func (h *Harness) Halted() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.halted
}

// Metrics returns a snapshot of the counters.
func (h *Harness) Metrics() Metrics {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.metrics
}

func (h *Harness) count(f func(*Metrics)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f(&h.metrics)
}

func (h *Harness) log(level slog.Level, msg string, err error, attempt int) {
	if h.Log == nil {
		return
	}
	h.Log.Log(context.Background(), level, msg,
		"tool", ToolName,
		"error_class", classifyError(err).String(),
		"attempt", attempt,
		"err", err.Error(),
	)
}

// Policy states the harness's contract in one line, from the constants that
// implement it rather than from a comment that can drift away from them. Printed
// at the top of a run, because a recovery rate means nothing without the ceiling
// it was measured against.
func (h *Harness) Policy() string {
	return fmt.Sprintf("retry up to %d times per call, surface up to %d rejected arguments to the model, everything else fatal",
		h.maxAttempts(), maxSelfCorrections)
}

// Line is the run's report in one line.
//
// It lives on the Harness rather than on Metrics because a fatal error is part of
// what the harness did, and a report that omitted it would read as a clean run.
// Every zero is named: "recovery 0%" and "no transient failure" are different
// facts, and only one of them is bad news.
func (h *Harness) Line() string {
	h.mu.Lock()
	m := h.metrics
	m.SelfCorrections = h.selfCorrections
	halted := h.halted
	h.mu.Unlock()

	parts := []string{
		plural(m.Calls, "tool call", "tool calls"),
		plural(m.Attempts, "acquirer attempt", "acquirer attempts"),
		plural(m.Transients, "transient failure", "transient failures"),
	}
	if m.Recovered+m.GivenUp == 0 {
		parts = append(parts, "recovery n/a (nothing failed)")
	} else {
		rate := 100 * float64(m.Recovered) / float64(m.Recovered+m.GivenUp)
		parts = append(parts, fmt.Sprintf("recovery %.0f%% (%d of %d)",
			rate, m.Recovered, m.Recovered+m.GivenUp))
	}
	parts = append(parts,
		fmt.Sprintf("retries-to-success %d", m.RetriesToSuccess),
		fmt.Sprintf("self-corrections %d", m.SelfCorrections),
	)
	if !m.diagnosed.IsZero() {
		parts = append(parts, "time-to-diagnosis "+m.diagnosed.Sub(m.firstFailure).Round(time.Millisecond).String())
	}
	if halted != nil {
		parts = append(parts, "HALTED: "+halted.Error())
	}
	return "harness: " + strings.Join(parts, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
