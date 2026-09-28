package examples

import (
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
)

// Re-entry HITL: Start → prepare → approve (pauses) → execute.
//
// Resume from a client with a user message holding
//
//	genai.FunctionResponse{ID: <InterruptID>, Name: "adk_request_input",
//		Response: map[string]any{"response": "approve"}}

type payout struct {
	Amount   string `json:"amount"`
	Decision string `json:"decision,omitempty"`
}

// NewApprovalWorkflow wires a durable human approval checkpoint.
func NewApprovalWorkflow() (agent.Agent, error) {
	// &true: re-run approve from the top on resume, so the code after
	// ResumeOrRequestInput executes. nil would hand the reply to execute.
	rerun := true

	approve := workflow.NewEmittingFunctionNode("approve",
		func(ctx agent.Context, in payout, emit func(*session.Event) error) (payout, error) {
			// No side effects above this line: this body runs twice.
			reply, err := workflow.ResumeOrRequestInput(ctx, emit, session.RequestInput{
				InterruptID: "approve-" + ctx.InvocationID(), // unique per run
				Message:     fmt.Sprintf("Approve payout of %s? (approve/reject)", in.Amount),
				Payload:     in,
			})
			if err != nil {
				return payout{}, err // first pass: ErrNodeInterrupted → run parks
			}
			// ResponseSchema is not enforced — validate the reply yourself.
			in.Decision = "rejected"
			if s, ok := reply.(string); ok && strings.EqualFold(strings.TrimSpace(s), "approve") {
				in.Decision = "approved"
			}
			return in, nil
		}, workflow.NodeConfig{RerunOnResume: &rerun})

	edges := workflow.Chain(
		workflow.Start,
		workflow.NewFunctionNode("prepare", func(_ agent.Context, in string) (payout, error) {
			return payout{Amount: in}, nil
		}, workflow.NodeConfig{}),
		approve,
		// The side effect lives after the resume point.
		workflow.NewFunctionNode("execute", func(_ agent.Context, p payout) (string, error) {
			if p.Decision != "approved" {
				return "not sent", nil
			}
			return "sent " + p.Amount, nil
		}, workflow.NodeConfig{}),
	)
	return workflowagent.New(workflowagent.Config{Name: "payout_approval", Edges: edges})
}
