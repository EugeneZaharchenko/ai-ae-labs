// Agent for Week 2, Part 1: WorkflowGraphAgent
// Explicit workflow graph with typed nodes — ADK 2.0 Chain pattern.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/internal/adkrun"
)

// RefundCaseInput is the typed input for opening a refund case.
type RefundCaseInput struct {
	TransactionID string `json:"transaction_id"`
	MerchantID    string `json:"merchant_id"`
}

// RefundCaseOutput is the typed output.
type RefundCaseOutput struct {
	CaseID        string `json:"case_id"`
	TransactionID string `json:"transaction_id"`
	MerchantID    string `json:"merchant_id"`
	Status        string `json:"status"`
}

// prepareNode extracts and validates input fields.
func prepareNode(ctx agent.Context, input string) (RefundCaseInput, error) {
	parts := strings.SplitN(input, ",", 2)
	if len(parts) < 2 {
		return RefundCaseInput{}, fmt.Errorf("expected format: transaction_id,merchant_id")
	}
	return RefundCaseInput{
		TransactionID: strings.ToLower(strings.TrimSpace(parts[0])),
		MerchantID:    strings.ToUpper(strings.TrimSpace(parts[1])),
	}, nil
}

// openRefundCase simulates the LEDGERWORKS refund API call.
func openRefundCase(ctx agent.Context, input RefundCaseInput) (RefundCaseOutput, error) {
	if input.TransactionID == "" || input.MerchantID == "" {
		return RefundCaseOutput{}, fmt.Errorf("transaction_id and merchant_id are required")
	}
	return RefundCaseOutput{
		CaseID:        "rc-" + input.TransactionID + "-" + input.MerchantID,
		TransactionID: input.TransactionID,
		MerchantID:    input.MerchantID,
		Status:        "pending",
	}, nil
}

// formatNode formats the output for display.
func formatNode(ctx agent.Context, input RefundCaseOutput) (string, error) {
	return fmt.Sprintf("Refund case %s for merchant %s (%s)", input.CaseID, input.MerchantID, input.Status), nil
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("workflow-graph", flag.ContinueOnError)
	input := fs.String("input", "txn-2026-07-118845,A-114", "transaction_id,merchant_id")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	a, err := adkrun.Pipeline(
		"WorkflowGraphAgent",
		"Explicit workflow graph with typed nodes — ADK 2.0 Chain pattern",
		workflow.NewFunctionNode[string, RefundCaseInput]("prepare", prepareNode, adkrun.NodeConfig()),
		workflow.NewFunctionNode[RefundCaseInput, RefundCaseOutput]("open_refund_case", openRefundCase, adkrun.NodeConfig()),
		workflow.NewFunctionNode[RefundCaseOutput, string]("format", formatNode, adkrun.NodeConfig()),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	out, err := adkrun.Run(context.Background(), a, *input, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Println(out)
	return 0
}
