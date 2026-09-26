package refund

import (
	"context"
	"strings"
	"sync"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
)

func TestPrepare(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  Input
		bad   bool
	}{
		{"Мерчант A-114: txn-2026-07-118845", Input{"txn-2026-07-118845", "A-114"}, false},
		{"TXN-123 для b-207.", Input{"txn-123", "B-207"}, false},
		{"", Input{}, true},
		{"A-114", Input{}, true},
		{"txn-123", Input{}, true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			ctx := agent.NewStrictContextMock(t.Context())
			got, err := Prepare(&ctx, tc.input)
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("Prepare = %+v, %v; want %+v, bad=%v", got, err, tc.want, tc.bad)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	valid := Output{"rc-txn-123-A-114", "txn-123", "A-114", "pending"}
	for _, tc := range []struct {
		name string
		in   Output
		bad  bool
	}{
		{"valid", valid, false},
		{"duplicate", Output{valid.CaseID, valid.TransactionID, valid.MerchantID, "already_open"}, false},
		{"empty", Output{}, true},
		{"missing transaction", Output{valid.CaseID, "", valid.MerchantID, valid.Status}, true},
		{"missing merchant", Output{valid.CaseID, valid.TransactionID, "", valid.Status}, true},
		{"invented status", Output{valid.CaseID, valid.TransactionID, valid.MerchantID, "paid"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := agent.NewStrictContextMock(t.Context())
			got, err := Format(&ctx, tc.in)
			if (err != nil) != tc.bad {
				t.Fatalf("Format = %q, %v", got, err)
			}
			if !tc.bad && (!strings.Contains(got, tc.in.CaseID) || !strings.Contains(got, tc.in.Status)) {
				t.Fatalf("lost result fields: %s", got)
			}
		})
	}
}

type toolContext struct {
	agent.StrictContextMock
	actions session.EventActions
}

func (c *toolContext) Actions() *session.EventActions { return &c.actions }

func TestToolStateAndIdempotency(t *testing.T) {
	reg := &Registry{}
	for _, tc := range []struct {
		in     Input
		status string
	}{
		{Input{"TXN-123", "a-114"}, "pending"},
		{Input{"txn-123", "A-114"}, "already_open"},
		{Input{"invalid", "A-114"}, ""},
		{Input{"txn-123", "Z-999"}, ""},
	} {
		ctx := &toolContext{StrictContextMock: agent.NewStrictContextMock(context.Background())}
		out, err := reg.OpenCase(ctx, tc.in)
		if tc.status == "" {
			if err == nil || len(ctx.actions.StateDelta) != 0 {
				t.Fatalf("rejected input mutated state: %+v, %v", ctx.actions, err)
			}
			continue
		}
		if err != nil || out.Status != tc.status || out.CaseID != "rc-txn-123-A-114" {
			t.Fatalf("OpenCase = %+v, %v", out, err)
		}
		if ctx.actions.StateDelta[StateKeyStatus] != tc.status || ctx.actions.StateDelta[StateKeyCaseID] != out.CaseID {
			t.Fatalf("missing audit: %v", ctx.actions.StateDelta)
		}
	}
	if len(reg.cases) != 1 {
		t.Fatalf("registry entries = %d", len(reg.cases))
	}
}

func TestConcurrentDuplicate(t *testing.T) {
	reg := &Registry{}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			ctx := &toolContext{StrictContextMock: agent.NewStrictContextMock(context.Background())}
			if _, err := reg.OpenCase(ctx, Input{"txn-123", "A-114"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(reg.cases) != 1 {
		t.Fatalf("registry entries = %d", len(reg.cases))
	}
}

func TestGraph(t *testing.T) {
	for _, tc := range []struct {
		input string
		bad   bool
	}{
		{"txn-123 A-114", false},
		{"hello", true},
		{"txn-123 Z-999", true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			reg := &Registry{}
			a, err := NewGraph(reg)
			if err != nil {
				t.Fatal(err)
			}
			res, err := labrun.Run(t.Context(), a, tc.input)
			if (err != nil) != tc.bad {
				t.Fatalf("run = %+v, %v", res, err)
			}
			if tc.bad && len(reg.cases) != 0 {
				t.Fatal("invalid request created a case")
			}
			if !tc.bad && !strings.Contains(res.Final, "rc-txn-123-A-114") {
				t.Fatalf("final = %q", res.Final)
			}
		})
	}
}
