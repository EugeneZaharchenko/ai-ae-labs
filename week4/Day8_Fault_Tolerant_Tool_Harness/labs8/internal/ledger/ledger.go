// Package ledger is the LEDGERWORKS domain the Day 8 lab runs against: typed
// tool contracts, the errors those tools fail with, a deterministic fault
// queue, and a refund desk whose side effect happens at most once per case.
//
// It is plumbing for the lab, not the assignment: students classify these
// errors; they do not edit them.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Error classes. Every ledger error matches exactly one of them with errors.Is.
var (
	// ErrTransient: the call may succeed if repeated (timeout, 503, rate limit,
	// dependency briefly down). Engines retry these.
	ErrTransient = errors.New("ledger: transient failure")
	// ErrSemantic: the call is wrong and repeating it changes nothing (missing
	// or malformed argument, failed precondition). The model must fix it.
	ErrSemantic = errors.New("ledger: semantic error")
)

// SemanticError says which argument is wrong and why.
type SemanticError struct {
	Field  string
	Reason string
}

func (e *SemanticError) Error() string {
	return fmt.Sprintf("invalid %s: %s", e.Field, e.Reason)
}

// Is makes errors.Is(err, ErrSemantic) true.
func (e *SemanticError) Is(target error) bool { return target == ErrSemantic }

// TransientError is a failure worth retrying.
type TransientError struct {
	Op         string
	Reason     string
	RetryAfter time.Duration // zero when the dependency gave no hint
}

func (e *TransientError) Error() string {
	return fmt.Sprintf("%s: %s", e.Op, e.Reason)
}

// Is makes errors.Is(err, ErrTransient) true.
func (e *TransientError) Is(target error) bool { return target == ErrTransient }

// RefundCaseInput is the contract of open_refund_case: exactly the two fields
// that identify a refund case.
type RefundCaseInput struct {
	TransactionID string `json:"transaction_id" jsonschema:"Transaction id, e.g. txn-2026-07-118845"`
	MerchantID    string `json:"merchant_id" jsonschema:"Merchant id, e.g. A-114"`
}

// RefundCaseOutput is what open_refund_case returns on success.
type RefundCaseOutput struct {
	Status   string `json:"status"`
	CaseID   string `json:"case_id"`
	Replayed bool   `json:"replayed"` // true: the case already existed, nothing new happened
}

var (
	txnRe      = regexp.MustCompile(`^txn-\d{4}-\d{2}-\d+$`)
	merchantRe = regexp.MustCompile(`^[A-Z]-\d{2,}$`)
)

// Validate rejects arguments a model typically gets wrong. "undefined" is
// checked explicitly: it is a valid non-empty string, which is exactly how it
// reached the production database at 03:00.
func (in RefundCaseInput) Validate() error {
	switch m := strings.TrimSpace(in.MerchantID); {
	case m == "" || strings.EqualFold(m, "undefined") || strings.EqualFold(m, "null"):
		return &SemanticError{Field: "merchant_id", Reason: fmt.Sprintf("%q was not extracted from the request", in.MerchantID)}
	case !merchantRe.MatchString(m):
		return &SemanticError{Field: "merchant_id", Reason: fmt.Sprintf("%q is not a merchant id like A-114", in.MerchantID)}
	}
	switch t := strings.TrimSpace(in.TransactionID); {
	case t == "" || strings.EqualFold(t, "undefined") || strings.EqualFold(t, "null"):
		return &SemanticError{Field: "transaction_id", Reason: fmt.Sprintf("%q was not extracted from the request", in.TransactionID)}
	case !txnRe.MatchString(t):
		return &SemanticError{Field: "transaction_id", Reason: fmt.Sprintf("%q is not a transaction id like txn-2026-07-118845", in.TransactionID)}
	}
	return nil
}

// CaseID is the idempotency key: one case per transaction per merchant.
func CaseID(in RefundCaseInput) string {
	return "rc-" + strings.TrimSpace(in.TransactionID) + "-" + strings.TrimSpace(in.MerchantID)
}

// Fault is one injected failure.
type Fault int

// Faults the plan can inject.
const (
	NoFault     Fault = iota
	Timeout           // acquirer did not answer in time
	Unavailable       // 503 from the acquirer
	RateLimited       // 429 with a Retry-After hint
)

// Err returns the error the fault produces for op.
func (f Fault) Err(op string) error {
	switch f {
	case Timeout:
		return &TransientError{Op: op, Reason: "acquirer timeout"}
	case Unavailable:
		return &TransientError{Op: op, Reason: "acquirer returned 503"}
	case RateLimited:
		return &TransientError{Op: op, Reason: "acquirer returned 429", RetryAfter: time.Second}
	}
	return nil
}

// FaultPlan is a FIFO queue of failures. Calls pop one fault each; an empty
// queue means success. A queue, not a probability: tests must fail the same
// way on every run. The zero value is an empty plan.
type FaultPlan struct {
	mu    sync.Mutex
	queue []Fault
}

// Push appends faults to the queue.
func (p *FaultPlan) Push(faults ...Fault) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queue = append(p.queue, faults...)
}

// Next pops the next fault, or NoFault.
func (p *FaultPlan) Next() Fault {
	if p == nil {
		return NoFault
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		return NoFault
	}
	f := p.queue[0]
	p.queue = p.queue[1:]
	return f
}

// Len reports how many faults are still queued.
func (p *FaultPlan) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue)
}

// Cases records opened refund cases. Open must be atomic per id: the first
// call creates the case, every later call returns it with replayed=true.
type Cases interface {
	Open(ctx context.Context, id string, in RefundCaseInput) (replayed bool, err error)
}

// MemCases is an in-process Cases for tests. The zero value works.
type MemCases struct {
	mu    sync.Mutex
	cases map[string]RefundCaseInput
}

// Open implements Cases.
func (m *MemCases) Open(_ context.Context, id string, in RefundCaseInput) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cases == nil {
		m.cases = map[string]RefundCaseInput{}
	}
	if _, ok := m.cases[id]; ok {
		return true, nil
	}
	m.cases[id] = in
	return false, nil
}

// Len reports how many distinct cases exist.
func (m *MemCases) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.cases)
}

// Desk opens refund cases against an unreliable acquirer.
type Desk struct {
	Cases  Cases
	Faults *FaultPlan
	// Latency simulates a slow acquirer, so a worker can be killed mid-call.
	Latency time.Duration
}

// OpenRefundCase validates, waits out Latency, consults the fault plan, and
// records the case once. Validation runs first: a semantic error is never
// worth a network round trip.
func (d *Desk) OpenRefundCase(ctx context.Context, in RefundCaseInput) (RefundCaseOutput, error) {
	if err := in.Validate(); err != nil {
		return RefundCaseOutput{}, err
	}
	if d.Latency > 0 {
		select {
		case <-ctx.Done():
			return RefundCaseOutput{}, ctx.Err()
		case <-time.After(d.Latency):
		}
	}
	if err := d.Faults.Next().Err("open_refund_case"); err != nil {
		return RefundCaseOutput{}, err
	}
	id := CaseID(in)
	replayed, err := d.Cases.Open(ctx, id, in)
	if err != nil {
		return RefundCaseOutput{}, &TransientError{Op: "open_refund_case", Reason: "case store: " + err.Error()}
	}
	return RefundCaseOutput{Status: "pending", CaseID: id, Replayed: replayed}, nil
}

// PayoutsInput is the contract of get_merchant_payouts.
type PayoutsInput struct {
	MerchantID string `json:"merchant_id" jsonschema:"Merchant id, e.g. A-114"`
}

// PayoutsOutput summarises a merchant's payouts.
type PayoutsOutput struct {
	MerchantID string `json:"merchant_id"`
	Summary    string `json:"summary"`
}

// GetMerchantPayouts is read-only and deterministic.
func GetMerchantPayouts(_ context.Context, in PayoutsInput) (PayoutsOutput, error) {
	m := strings.TrimSpace(in.MerchantID)
	if !merchantRe.MatchString(m) {
		return PayoutsOutput{}, &SemanticError{Field: "merchant_id", Reason: fmt.Sprintf("%q is not a merchant id like A-114", in.MerchantID)}
	}
	if m != "A-114" {
		return PayoutsOutput{MerchantID: m, Summary: "no pending payouts"}, nil
	}
	return PayoutsOutput{MerchantID: m, Summary: "2 payouts pending since 03:10; last settled batch #418 delayed, acquirer says unrelated"}, nil
}
