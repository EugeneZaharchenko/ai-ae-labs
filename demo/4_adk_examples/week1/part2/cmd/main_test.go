package main

import (
	"context"
	"testing"

	"google.golang.org/adk/v2/agent"
)

func validReq() RefundCaseRequest {
	return RefundCaseRequest{
		TransactionID: "txn-2026-07-118845",
		MerchantID:    "A-114",
		ReasonCode:    "customer_request",
		AmountMinor:   145000,
	}
}

func TestValidateRefundCaseInput_Valid(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	resp, err := validateRefundCaseInput(ctx, validReq())
	if err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
	if resp.RefundCaseID == "" {
		t.Fatal("expected non-empty refund case ID")
	}
	if resp.Status != "pending_review" {
		t.Fatalf("expected status 'pending_review', got %q", resp.Status)
	}
}

func TestValidateRefundCaseInput_Rejects(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	tests := []struct {
		name   string
		mutate func(*RefundCaseRequest)
	}{
		{"missing transaction_id", func(r *RefundCaseRequest) { r.TransactionID = "" }},
		{"missing merchant_id", func(r *RefundCaseRequest) { r.MerchantID = "" }},
		{"reason_code outside catalogue", func(r *RefundCaseRequest) { r.ReasonCode = "fraud" }},
		{"empty reason_code", func(r *RefundCaseRequest) { r.ReasonCode = "" }},
		{"zero amount", func(r *RefundCaseRequest) { r.AmountMinor = 0 }},
		{"negative amount", func(r *RefundCaseRequest) { r.AmountMinor = -1 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := validReq()
			tc.mutate(&req)
			if _, err := validateRefundCaseInput(ctx, req); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestFormatResponse(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	resp := RefundCaseResponse{RefundCaseID: "rc-txn-2026-07-118845-A-114", Status: "pending_review"}
	out, err := formatResponse(ctx, resp)
	if err != nil {
		t.Fatalf("format failed: %v", err)
	}
	if !contains(out, "rc-txn-2026-07-118845-A-114") {
		t.Errorf("expected refund case ID in output, got: %s", out)
	}
}
