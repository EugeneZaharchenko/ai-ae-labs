package examples

import (
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
)

// NewCoordinator wires C1: a coordinator that delegates to specialists chosen
// at run time. Each specialist runs in ModeSingleTurn, so the coordinator sees
// it as a TOOL named after the agent with args {"request": string}, and the
// specialist gets its own isolated turn — the coordinator's context does not
// grow with the specialist's history.
//
// Offline, a rule-based brain for the coordinator answers
//
//	kit.Call("billing_agent", map[string]any{"request": p.Text})
//
// and, once p.Result("billing_agent") exists, summarizes it.
func NewCoordinator(coordinatorModel, specialistModel model.LLM) (agent.Agent, error) {
	billing, err := llmagent.New(llmagent.Config{
		Name:        "billing_agent",
		Description: "Answers invoice, refund and payment questions.",
		Model:       specialistModel,
		Instruction: "Answer the billing request in two sentences.",
		Mode:        llmagent.ModeSingleTurn,
	})
	if err != nil {
		return nil, err
	}
	tech, err := llmagent.New(llmagent.Config{
		Name:        "tech_agent",
		Description: "Diagnoses crashes, errors and login problems.",
		Model:       specialistModel,
		Instruction: "Diagnose the technical request and give one next step.",
		Mode:        llmagent.ModeSingleTurn,
	})
	if err != nil {
		return nil, err
	}
	return llmagent.New(llmagent.Config{
		Name:  "coordinator",
		Model: coordinatorModel,
		Instruction: `Route the request to exactly one specialist tool:
billing_agent for money questions, tech_agent for technical problems.
Then answer the user using only the specialist's result.`,
		SubAgents: []agent.Agent{billing, tech},
	})
}
