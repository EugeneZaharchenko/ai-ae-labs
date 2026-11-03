package main

import (
	"context"
	"testing"

	"google.golang.org/adk/v2/agent"
)

func TestPrepareNode_Valid(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	input, err := prepareNode(ctx, "txn-2026-07-118845,A-114")
	if err != nil {
		t.Fatalf("prepareNode failed: %v", err)
	}
	if input.TransactionID != "txn-2026-07-118845" {
		t.Errorf("expected transaction_id 'txn-2026-07-118845', got %q", input.TransactionID)
	}
	if input.MerchantID != "A-114" {
		t.Errorf("expected merchant_id 'A-114', got %q", input.MerchantID)
	}
}

func TestPrepareNode_Invalid(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	_, err := prepareNode(ctx, "only-one-field")
	if err == nil {
		t.Fatal("expected error for invalid input format")
	}
}

func TestOpenRefundCase_Valid(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	input := RefundCaseInput{TransactionID: "txn-2026-07-118845", MerchantID: "A-114"}
	out, err := openRefundCase(ctx, input)
	if err != nil {
		t.Fatalf("openRefundCase failed: %v", err)
	}
	if out.Status != "pending" {
		t.Errorf("expected status 'pending', got %q", out.Status)
	}
	if out.CaseID != "rc-txn-2026-07-118845-A-114" {
		t.Errorf("expected case id 'rc-txn-2026-07-118845-A-114', got %q", out.CaseID)
	}
}

func TestOpenRefundCase_MissingFields(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	_, err := openRefundCase(ctx, RefundCaseInput{})
	if err == nil {
		t.Fatal("expected error for missing fields")
	}
}

func TestFormatNode(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	out, err := formatNode(ctx, RefundCaseOutput{
		CaseID: "rc-txn-1-A-114", TransactionID: "txn-1", MerchantID: "A-114", Status: "pending",
	})
	if err != nil {
		t.Fatalf("formatNode failed: %v", err)
	}
	if !contains(out, "rc-txn-1-A-114") || !contains(out, "pending") {
		t.Errorf("unexpected format: %s", out)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
