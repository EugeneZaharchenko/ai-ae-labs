// E1 · Human-in-the-Loop — the workflow parks at a checkpoint, shows a human
// structured context, and resumes with the answer. Durable: the pause is an
// event in the session, not a goroutine blocked on stdin.
//
//	Start → prepare_payout → approve (ResumeOrRequestInput) → execute_payout
//
//	go run .            # scripted demo: pause, human answers "approve", resume
//	go run . console    # you are the human
package main

import (
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:      "E1",
	Title:   "Human-in-the-Loop",
	Input:   "Pay out 1200 EUR to merchant A-114",
	Answers: []any{"approve"},
	Build:   build,
}

func main() { kit.Main(spec) }

// payout is the structured context a human signs off on.
type payout struct {
	Merchant string `json:"merchant"`
	Amount   string `json:"amount"`
	Decision string `json:"decision,omitempty"`
}

func prepare(_ agent.Context, in string) (payout, error) {
	p := payout{Merchant: "unknown", Amount: "unknown"}
	for _, f := range strings.Fields(in) {
		switch {
		case strings.HasPrefix(f, "A-"):
			p.Merchant = f
		case strings.ContainsAny(f, "0123456789") && p.Amount == "unknown":
			p.Amount = f + " EUR"
		}
	}
	return p, nil
}

// approve pauses on the first pass and returns the human's reply after
// resume. RerunOnResume=true re-enters this node from the top, so it must be
// free of side effects before the pause — the payout happens in the NEXT node.
func approve(ctx agent.Context, in payout, emit func(*session.Event) error) (payout, error) {
	reply, err := workflow.ResumeOrRequestInput(ctx, emit, session.RequestInput{
		InterruptID: "approve-payout-" + ctx.InvocationID(),
		Message:     fmt.Sprintf("Approve payout of %s to %s? (approve/reject)", in.Amount, in.Merchant),
		Payload:     in,
	})
	if err != nil {
		return payout{}, err
	}
	// ResponseSchema is not enforced by the runtime: validate the reply here.
	in.Decision = "rejected"
	if s, ok := reply.(string); ok && strings.EqualFold(strings.TrimSpace(s), "approve") {
		in.Decision = "approved"
	}
	return in, nil
}

func execute(_ agent.Context, in payout) (string, error) {
	if in.Decision != "approved" {
		return fmt.Sprintf("payout to %s NOT sent (human said %s)", in.Merchant, in.Decision), nil
	}
	return fmt.Sprintf("payout of %s to %s sent", in.Amount, in.Merchant), nil
}

func build(kit.Models) (agent.Agent, error) {
	rerun := true
	edges := workflow.Chain(
		workflow.Start,
		workflow.NewFunctionNode("prepare_payout", prepare, workflow.NodeConfig{}),
		workflow.NewEmittingFunctionNode("approve", approve, workflow.NodeConfig{RerunOnResume: &rerun}),
		workflow.NewFunctionNode("execute_payout", execute, workflow.NodeConfig{}),
	)
	return workflowagent.New(workflowagent.Config{
		Name:        "payout_approval",
		Description: "Payout with a durable human approval checkpoint.",
		Edges:       edges,
	})
}
