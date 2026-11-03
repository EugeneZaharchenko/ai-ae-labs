// Agent for Week 1, Part 2: StrictSchemaEnforcer
// Typed Go contracts with JSON Schema validation for structured output.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/internal/adkrun"
)

// RefundCaseRequest is the typed contract for opening a refund case.
type RefundCaseRequest struct {
	TransactionID string `json:"transaction_id" jsonschema:"required,description=Transaction ID from the LEDGERWORKS registry"`
	MerchantID    string `json:"merchant_id" jsonschema:"required,description=Merchant code, e.g. A-114"`
	ReasonCode    string `json:"reason_code" jsonschema:"required,enum=customer_request,enum=chargeback,enum=duplicate"`
	AmountMinor   int64  `json:"amount_minor" jsonschema:"required,description=Refund amount in minor units (kopiykas)"`
}

// RefundCaseResponse is the typed response.
type RefundCaseResponse struct {
	RefundCaseID string `json:"refund_case_id"`
	Status       string `json:"status"`
}

// reasonCodes is the closed catalogue behind the ReasonCode enum. The schema
// keeps the model from inventing a fourth code; this slice is what the domain
// boundary checks after the schema has done its job.
var reasonCodes = []string{"customer_request", "chargeback", "duplicate"}

// parseRefundCaseInput decodes the user message into the typed contract. The
// engine hands the first node the raw message as a string, so the typed edge
// starts here. Unknown fields are rejected: that is the "strict" in the name.
func parseRefundCaseInput(ctx agent.Context, raw string) (RefundCaseRequest, error) {
	var req RefundCaseRequest
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return RefundCaseRequest{}, fmt.Errorf("input is not a RefundCaseRequest: %w", err)
	}
	return req, nil
}

// validateRefundCaseInput validates a refund-case request.
func validateRefundCaseInput(ctx agent.Context, req RefundCaseRequest) (RefundCaseResponse, error) {
	if req.TransactionID == "" {
		return RefundCaseResponse{}, fmt.Errorf("transaction_id is required")
	}
	if req.MerchantID == "" {
		return RefundCaseResponse{}, fmt.Errorf("merchant_id is required")
	}
	if !slices.Contains(reasonCodes, req.ReasonCode) {
		return RefundCaseResponse{}, fmt.Errorf("reason_code %q is not in the catalogue %v", req.ReasonCode, reasonCodes)
	}
	if req.AmountMinor <= 0 {
		return RefundCaseResponse{}, fmt.Errorf("amount_minor must be positive minor units, got %d", req.AmountMinor)
	}
	return RefundCaseResponse{
		RefundCaseID: "rc-" + req.TransactionID + "-" + req.MerchantID,
		Status:       "pending_review",
	}, nil
}

// formatResponse formats the refund-case result.
func formatResponse(ctx agent.Context, resp RefundCaseResponse) (string, error) {
	b, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	return string(b), nil
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("schema-enforcer", flag.ContinueOnError)
	txnID := fs.String("txn", "txn-2026-07-118845", "transaction ID")
	merchantID := fs.String("merchant", "A-114", "merchant ID")
	reason := fs.String("reason", "customer_request", "reason code: customer_request|chargeback|duplicate")
	amountMinor := fs.Int64("amount-minor", 145000, "refund amount in minor units (kopiykas)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	a, err := adkrun.Pipeline(
		"StrictSchemaEnforcer",
		"Typed Go contracts with JSON Schema validation for structured output",
		workflow.NewFunctionNode[string, RefundCaseRequest]("parse", parseRefundCaseInput, adkrun.NodeConfig()),
		workflow.NewFunctionNode[RefundCaseRequest, RefundCaseResponse]("validate", validateRefundCaseInput, adkrun.NodeConfig()),
		workflow.NewFunctionNode[RefundCaseResponse, string]("format", formatResponse, adkrun.NodeConfig()),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	input := RefundCaseRequest{
		TransactionID: *txnID,
		MerchantID:    *merchantID,
		ReasonCode:    *reason,
		AmountMinor:   *amountMinor,
	}
	// The first node takes a typed RefundCaseRequest, and the engine decodes the
	// user message as JSON into it, so send JSON, not a Go struct dump.
	payload, err := json.Marshal(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	out, err := adkrun.Run(context.Background(), a, string(payload), os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Println(out)
	return 0
}
