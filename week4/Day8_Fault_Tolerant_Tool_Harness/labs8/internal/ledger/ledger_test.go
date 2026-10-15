package ledger

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var good = RefundCaseInput{TransactionID: "txn-2026-07-118845", MerchantID: "A-114"}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		in    RefundCaseInput
		field string // "" = valid
	}{
		{"valid", good, ""},
		{"undefined merchant", RefundCaseInput{good.TransactionID, "undefined"}, "merchant_id"},
		{"empty merchant", RefundCaseInput{good.TransactionID, " "}, "merchant_id"},
		{"lowercase merchant", RefundCaseInput{good.TransactionID, "a-114"}, "merchant_id"},
		{"null transaction", RefundCaseInput{"null", "A-114"}, "transaction_id"},
		{"malformed transaction", RefundCaseInput{"118845", "A-114"}, "transaction_id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.in.Validate()
			if tc.field == "" {
				if err != nil {
					t.Fatalf("valid input rejected: %v", err)
				}
				return
			}
			var se *SemanticError
			if !errors.As(err, &se) || se.Field != tc.field || !errors.Is(err, ErrSemantic) || errors.Is(err, ErrTransient) {
				t.Fatalf("err = %v, want SemanticError on %s", err, tc.field)
			}
		})
	}
}

func TestFaultsAreTransient(t *testing.T) {
	t.Parallel()
	for _, f := range []Fault{Timeout, Unavailable, RateLimited} {
		err := f.Err("op")
		if !errors.Is(err, ErrTransient) || errors.Is(err, ErrSemantic) || !strings.HasPrefix(err.Error(), "op: ") {
			t.Errorf("fault %d: err = %v", f, err)
		}
	}
	var te *TransientError
	if errors.As(RateLimited.Err("op"), &te); te.RetryAfter != time.Second {
		t.Errorf("rate limit RetryAfter = %v", te.RetryAfter)
	}
	if NoFault.Err("op") != nil {
		t.Error("NoFault must not fail")
	}
}

func TestFaultPlanIsFIFO(t *testing.T) {
	t.Parallel()
	var p FaultPlan
	p.Push(Unavailable, Timeout)
	if p.Len() != 2 || p.Next() != Unavailable || p.Next() != Timeout || p.Next() != NoFault {
		t.Fatal("plan must pop faults in order, then succeed")
	}
	var nilPlan *FaultPlan
	if nilPlan.Next() != NoFault {
		t.Error("nil plan must mean no faults")
	}
}

func TestDeskRetriesDoNotDuplicate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := &MemCases{}
	faults := &FaultPlan{}
	faults.Push(Unavailable, Unavailable)
	d := &Desk{Cases: cases, Faults: faults}

	var attempts int
	var out RefundCaseOutput
	var err error
	for attempts = 1; attempts <= 5; attempts++ { // what a retrying engine does
		if out, err = d.OpenRefundCase(ctx, good); err == nil {
			break
		}
		if !errors.Is(err, ErrTransient) {
			t.Fatalf("attempt %d: %v", attempts, err)
		}
	}
	if attempts != 3 || out.CaseID != "rc-txn-2026-07-118845-A-114" || out.Replayed || out.Status != "pending" {
		t.Fatalf("after %d attempts: %+v, %v", attempts, out, err)
	}
	again, err := d.OpenRefundCase(ctx, good)
	if err != nil || !again.Replayed || cases.Len() != 1 {
		t.Fatalf("repeat = %+v, %v; cases = %d", again, err, cases.Len())
	}
}

func TestDeskSemanticBeforeFaults(t *testing.T) {
	t.Parallel()
	faults := &FaultPlan{}
	faults.Push(Unavailable)
	d := &Desk{Cases: &MemCases{}, Faults: faults}
	_, err := d.OpenRefundCase(context.Background(), RefundCaseInput{good.TransactionID, "undefined"})
	if !errors.Is(err, ErrSemantic) || faults.Len() != 1 {
		t.Fatalf("err = %v, queued faults = %d; validation must run before the network", err, faults.Len())
	}
}

type brokenCases struct{}

func (brokenCases) Open(context.Context, string, RefundCaseInput) (bool, error) {
	return false, errors.New("disk full")
}

func TestDeskCaseStoreFailureIsTransient(t *testing.T) {
	t.Parallel()
	_, err := (&Desk{Cases: brokenCases{}}).OpenRefundCase(context.Background(), good)
	if !errors.Is(err, ErrTransient) || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeskLatencyHonoursContext(t *testing.T) {
	t.Parallel()
	d := &Desk{Cases: &MemCases{}, Latency: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.OpenRefundCase(ctx, good); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	fast := &Desk{Cases: &MemCases{}, Latency: time.Millisecond}
	if _, err := fast.OpenRefundCase(context.Background(), good); err != nil {
		t.Fatal(err)
	}
}

func TestGetMerchantPayouts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if out, err := GetMerchantPayouts(ctx, PayoutsInput{"A-114"}); err != nil || !strings.Contains(out.Summary, "#418") {
		t.Errorf("A-114 = %+v, %v", out, err)
	}
	if out, err := GetMerchantPayouts(ctx, PayoutsInput{"B-200"}); err != nil || out.Summary != "no pending payouts" {
		t.Errorf("B-200 = %+v, %v", out, err)
	}
	if _, err := GetMerchantPayouts(ctx, PayoutsInput{"undefined"}); !errors.Is(err, ErrSemantic) {
		t.Errorf("bad id err = %v", err)
	}
}
