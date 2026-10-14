package main

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week2/internal/refund"
)

// HW3: tests for the nodes of the graph on agent.NewStrictContextMock.

// mockCtx is a strict mock that also keeps the EventActions, because the
// tool handler writes StateDelta through ctx.Actions().
type mockCtx struct {
	agent.StrictContextMock
	actions session.EventActions
}

func (c *mockCtx) Actions() *session.EventActions { return &c.actions }

func newMockCtx() *mockCtx {
	return &mockCtx{StrictContextMock: agent.NewStrictContextMock(context.Background())}
}

func TestPrepareNode(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    refund.Input
		wantErr bool
	}{
		{"free text", "Мерчант A-114 просить повернення по транзакції txn-2026-07-118845",
			refund.Input{TransactionID: "txn-2026-07-118845", MerchantID: "A-114"}, false},
		{"merchant in lower case", "txn-2026-07-118845 a-114",
			refund.Input{TransactionID: "txn-2026-07-118845", MerchantID: "A-114"}, false},
		{"empty input", "", refund.Input{}, true},
		{"no merchant", "повернення по txn-2026-07-118845", refund.Input{}, true},
		{"no transaction", "мерчант A-114 хоче гроші назад", refund.Input{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := agent.NewStrictContextMock(t.Context())
			got, err := refund.Prepare(&ctx, tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Prepare(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Prepare(%q) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatNode(t *testing.T) {
	ok := refund.Output{
		CaseID:        "rc-txn-2026-07-118845-A-114",
		TransactionID: "txn-2026-07-118845",
		MerchantID:    "A-114",
		Status:        "pending",
	}
	again := ok
	again.Status = "already_open"
	noCase := ok
	noCase.CaseID = ""
	badStatus := ok
	badStatus.Status = "refunded"

	tests := []struct {
		name    string
		in      refund.Output
		want    string
		wantErr bool
	}{
		{"new case", ok, "Кейс rc-txn-2026-07-118845-A-114: транзакція txn-2026-07-118845, мерчант A-114, статус pending", false},
		{"already open", again, "статус already_open", false},
		{"empty output", refund.Output{}, "", true},
		{"no case id", noCase, "", true},
		{"unknown status", badStatus, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := agent.NewStrictContextMock(t.Context())
			got, err := refund.Format(&ctx, tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Format() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("Format() = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

// The handler of open_refund_case: on success StateDelta has the case,
// on error StateDelta stays empty (otherwise the audit would lie).
func TestOpenRefundCaseStateDelta(t *testing.T) {
	tests := []struct {
		name       string
		in         refund.Input
		wantStatus string
		wantErr    bool
	}{
		{"fixture case", refund.Input{TransactionID: "txn-2026-07-118845", MerchantID: "A-114"}, "pending", false},
		{"unknown merchant", refund.Input{TransactionID: "txn-2026-07-118845", MerchantID: "Z-999"}, "", true},
		{"bad transaction id", refund.Input{TransactionID: "118845", MerchantID: "A-114"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &refund.Registry{}
			ctx := newMockCtx()
			out, err := reg.OpenCase(ctx, tt.in)
			delta := ctx.actions.StateDelta
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", out)
				}
				if len(delta) != 0 {
					t.Fatalf("StateDelta must be empty on error, got %v", delta)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if delta[refund.StateKeyCaseID] != demoCaseID {
				t.Errorf("case id in StateDelta = %v, want %s", delta[refund.StateKeyCaseID], demoCaseID)
			}
			if delta[refund.StateKeyStatus] != tt.wantStatus {
				t.Errorf("status in StateDelta = %v, want %s", delta[refund.StateKeyStatus], tt.wantStatus)
			}
		})
	}
}

// The static chain from agent_graph.go should give the case and leave
// it in the event log.
func TestStaticGraph(t *testing.T) {
	a, err := newStaticGraph(&refund.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := labrun.Run(t.Context(), a, demoInput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Final, demoCaseID) {
		t.Errorf("final = %q, want case id %s", res.Final, demoCaseID)
	}
	if !deltaCarries(res, refund.StateKeyStatus, "pending") {
		t.Errorf("no pending status in the event log: %+v", res.Events)
	}

	// wrong merchant -> the graph must fail, not answer
	a, err = newStaticGraph(&refund.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := labrun.Run(t.Context(), a, "Мерчант Z-999, транзакція txn-2026-07-118845"); err == nil {
		t.Error("expected error for unknown merchant")
	}
}
