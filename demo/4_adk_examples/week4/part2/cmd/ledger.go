package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The LEDGERWORKS refund desk: the one tool this day is about, and the acquirer
// behind it.
//
// Week 4 part 1's agent INVESTIGATED — it read the payout desk and reported.
// This one ACTS: open_refund_case opens a case with the acquirer. That difference
// is the whole reason this part exists. An investigation that fails can be
// retried by the person reading the answer; a state change that half-happens at
// 03:00 cannot, and a model that has lost track of the conversation will happily
// ask for it a second time.
//
// Names, ids and error types match the labs' internal/ledger (week 7 onward) so
// the demo and the graded lab are the same domain rather than two dialects of
// one. The fault plan is deterministic for the same reason the lab's is: a
// failing test that fails differently on every run teaches nothing.

// The two error classes the harness distinguishes, as sentinels rather than
// strings, so errors.Is works through any number of %w wrappings. Nothing in this
// file compares error text.
var (
	// ErrTransient marks a failure that is worth trying again: the acquirer timed
	// out, was unavailable, or rate-limited us. The same request may well succeed.
	ErrTransient = errors.New("ledger: transient failure")

	// ErrSemantic marks a failure that will fail identically forever: the request
	// itself is wrong. Retrying it burns the budget and hides the real problem.
	ErrSemantic = errors.New("ledger: semantic error")
)

// SemanticError names the field the model got wrong. The field name is the
// actionable part — "invalid argument" alone leaves the model guessing again.
type SemanticError struct {
	Field  string
	Reason string
}

func (e *SemanticError) Error() string { return fmt.Sprintf("invalid %s: %s", e.Field, e.Reason) }

// Is makes errors.Is(err, ErrSemantic) true through a wrapping chain, which is
// what the classifier keys on.
func (e *SemanticError) Is(target error) bool { return target == ErrSemantic }

// TransientError carries when the dependency says it will be ready again.
// RetryAfter is the acquirer's own answer to "when", and a harness that ignores
// it and retries immediately is the reason a struggling service stays down.
type TransientError struct {
	Op         string
	Reason     string
	RetryAfter time.Duration
}

func (e *TransientError) Error() string {
	return fmt.Sprintf("%s: %s", e.Op, e.Reason)
}

func (e *TransientError) Is(target error) bool { return target == ErrTransient }

// RefundCaseInput is what the model sends. The json and jsonschema tags are what
// ADK's functiontool turns into the declaration the provider sees, so this struct
// is simultaneously the tool's contract and its documentation.
type RefundCaseInput struct {
	TransactionID string `json:"transaction_id" jsonschema:"Transaction id, e.g. txn-2026-07-118845"`
	MerchantID    string `json:"merchant_id" jsonschema:"Merchant id, e.g. A-114"`
}

// RefundCaseOutput is what the tool returns. ADK converts it to a flat map of
// these json fields in the FunctionResponse — not wrapped under "result" — which
// is why the harness passes it through unchanged and the offline model reads
// reply["status"] directly.
type RefundCaseOutput struct {
	Status   string `json:"status"`             // pending, invalid_argument, unavailable, stopped
	CaseID   string `json:"case_id,omitempty"`  // set when the case was opened
	Replayed bool   `json:"replayed,omitempty"` // true when the case already existed
}

// Validation patterns. The anchors matter: this is a check on a whole field, not
// a search through prose.
var (
	txnRe      = regexp.MustCompile(`^txn-\d{4}-\d{2}-\d+$`)
	merchantRe = regexp.MustCompile(`^[A-Z]-\d{2,}$`)
)

// Validate rejects a request before anything else happens.
//
// The interesting case is the third one. "undefined" is a non-empty string, it
// passes every type check, and it satisfies a schema that only says "string" —
// which is exactly how it reached the production database at 03:00 when a model
// had no merchant id and wrote the word instead of asking for one. A validator
// that only checks for emptiness is a validator that lets that through.
func (in RefundCaseInput) Validate() error {
	merchant := strings.TrimSpace(in.MerchantID)
	switch {
	case merchant == "":
		return &SemanticError{Field: "merchant_id", Reason: "is empty"}
	case strings.EqualFold(merchant, "undefined"), strings.EqualFold(merchant, "null"):
		return &SemanticError{
			Field:  "merchant_id",
			Reason: fmt.Sprintf("%q is not a merchant id — the request did not contain one, so re-read it or ask the user", merchant),
		}
	case !merchantRe.MatchString(merchant):
		return &SemanticError{
			Field:  "merchant_id",
			Reason: fmt.Sprintf("%q is not a merchant id (they look like A-114)", merchant),
		}
	}

	txn := strings.TrimSpace(in.TransactionID)
	switch {
	case txn == "":
		return &SemanticError{Field: "transaction_id", Reason: "is empty"}
	case strings.EqualFold(txn, "undefined"), strings.EqualFold(txn, "null"):
		return &SemanticError{
			Field:  "transaction_id",
			Reason: fmt.Sprintf("%q is not a transaction id — the request did not contain one, so re-read it or ask the user", txn),
		}
	case !txnRe.MatchString(txn):
		return &SemanticError{
			Field:  "transaction_id",
			Reason: fmt.Sprintf("%q is not a transaction id (they look like txn-2026-07-118845)", txn),
		}
	}
	return nil
}

// CaseID is derived from the request rather than generated, so asking twice for
// the same refund converges on one case instead of two. Idempotency by
// construction beats idempotency by a lookup table.
func CaseID(in RefundCaseInput) string {
	return "rc-" + in.TransactionID + "-" + in.MerchantID
}

// Fault is one way the acquirer can fail. Explicit values, not probabilities:
// a test that fails 30% of the time is a test nobody trusts.
type Fault int

const (
	NoFault Fault = iota
	Timeout
	Unavailable
	RateLimited
	// Corrupt is this example's addition. The labs' three faults are all
	// transient, which means the fatal class in classifyError is unreachable by
	// running the code — you can only read about it. One unclassified failure
	// makes the third row of the decision table something a learner can watch
	// happen: an error nobody anticipated, which must stop the run.
	Corrupt
)

// Err is the failure this fault produces.
func (f Fault) Err(op string) error {
	switch f {
	case Timeout:
		return &TransientError{Op: op, Reason: "acquirer timeout after 8s"}
	case Unavailable:
		return &TransientError{Op: op, Reason: "acquirer returned 503"}
	case RateLimited:
		return &TransientError{Op: op, Reason: "acquirer rate limited (429)", RetryAfter: time.Second}
	case Corrupt:
		// Deliberately not a TransientError and not a SemanticError. Retrying
		// changes nothing and the request was well-formed, so this is neither of
		// the two classes the code knows about — which is the point.
		return errors.New("acquirer returned a body we could not parse")
	}
	return nil
}

func (f Fault) String() string {
	switch f {
	case Timeout:
		return "timeout"
	case Unavailable:
		return "503"
	case RateLimited:
		return "429"
	case Corrupt:
		return "corrupt"
	}
	return "none"
}

// parseFaults reads the -faults flag: a comma-separated queue of failures to
// inject, in order.
func parseFaults(spec string) ([]Fault, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	var out []Fault
	for _, name := range strings.Split(spec, ",") {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "timeout", "deadline":
			out = append(out, Timeout)
		case "503", "unavailable":
			out = append(out, Unavailable)
		case "429", "ratelimited":
			out = append(out, RateLimited)
		case "corrupt":
			out = append(out, Corrupt)
		default:
			return nil, fmt.Errorf("unknown fault %q (want timeout, 503, 429 or corrupt)", name)
		}
	}
	return out, nil
}

// FaultPlan is a queue of failures, not a probability.
//
// The lecture's line is "збій — це дані": a fault is data. A queue is what makes
// that true — the exact sequence of failures is an input to the run, so the same
// command produces the same run every time, and a bug found once stays found.
type FaultPlan struct {
	mu     sync.Mutex
	faults []Fault
}

// Push appends failures to the queue.
func (p *FaultPlan) Push(faults ...Fault) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.faults = append(p.faults, faults...)
}

// Next pops the next failure, or NoFault when the queue is empty. A nil plan
// means "nothing fails", so every call site can skip a nil check.
func (p *FaultPlan) Next() Fault {
	if p == nil {
		return NoFault
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.faults) == 0 {
		return NoFault
	}
	f := p.faults[0]
	p.faults = p.faults[1:]
	return f
}

// Len reports how many failures are still queued. A run that ends with faults
// left over is a run that did not exercise what the operator asked for, and a
// printed zero is what makes that visible rather than assumed.
func (p *FaultPlan) Len() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.faults)
}

// Cases is the acquirer's case store.
type Cases interface {
	Open(ctx context.Context, id string, in RefundCaseInput) (replayed bool, err error)
}

// MemCases is an in-memory case store. The zero value works.
type MemCases struct {
	mu   sync.Mutex
	open map[string]RefundCaseInput
}

// Open records a case once. The second identical call reports replayed instead
// of creating a second case — the behaviour that makes the derived CaseID worth
// having.
func (m *MemCases) Open(_ context.Context, id string, in RefundCaseInput) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.open == nil {
		m.open = map[string]RefundCaseInput{}
	}
	if _, ok := m.open[id]; ok {
		return true, nil
	}
	m.open[id] = in
	return false, nil
}

// Count reports how many distinct cases exist. It is the assertion that matters
// in a test: the tool's effect is the case that was opened, not the prose it
// returned.
func (m *MemCases) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.open)
}

// Desk is the acquirer. It is deliberately thin: validate, then fail according
// to the plan, then record.
type Desk struct {
	Cases  Cases
	Faults *FaultPlan
}

// OpenRefundCase is the dependency call the harness wraps.
//
// The order of the first two steps is load-bearing. Validation runs BEFORE the
// fault plan is consulted, so a malformed request never consumes a queued fault —
// a semantic error is not worth a network round trip, and it should not cost the
// run one of its scripted outages either.
func (d *Desk) OpenRefundCase(ctx context.Context, in RefundCaseInput) (RefundCaseOutput, error) {
	if err := in.Validate(); err != nil {
		return RefundCaseOutput{}, err
	}
	if err := ctx.Err(); err != nil {
		return RefundCaseOutput{}, &TransientError{Op: "open_refund_case", Reason: "the run was cancelled"}
	}
	if fault := d.Faults.Next(); fault != NoFault {
		return RefundCaseOutput{}, fault.Err("open_refund_case")
	}

	id := CaseID(in)
	replayed, err := d.Cases.Open(ctx, id, in)
	if err != nil {
		return RefundCaseOutput{}, err
	}
	return RefundCaseOutput{Status: "pending", CaseID: id, Replayed: replayed}, nil
}

// The identifiers this example's default task carries. The model has to lift
// them out of prose, which is the ordinary case and the one that produced the
// 03:00 incident: when it cannot find one, the temptation is to write something.
const (
	defaultTxn      = "txn-2026-07-118845"
	defaultMerchant = "A-114"
)

// defaultTask is deliberately a sentence rather than a form. Nothing in the tool
// signature tells the model which substring is the transaction id.
const defaultTask = "A customer of merchant " + defaultMerchant + " says their refund never arrived. " +
	"Open a refund case for transaction " + defaultTxn + "."
