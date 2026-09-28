// C1 · Coordinator / Dispatcher — a central LlmAgent reads each request and
// delegates it at runtime to a specialised sub-agent. The sub-agents run in
// ModeSingleTurn: each gets its own isolated branch and returns one result,
// so the coordinator's context does not grow with their internals.
//
//	coordinator ─(billing_agent)→ billing_agent ─┐
//	            └(tech_agent)───→ tech_agent ────┴→ coordinator summarises
//
//	go run .                                   # scripted demo, offline
//	go run . -input "The app crashes on login" # another sub-agent
//	go run . console
package main

import (
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:    "C1",
	Title: "Coordinator / Dispatcher",
	Input: "I was charged twice for my subscription this month",
	Build: build,
}

func main() { kit.Main(spec) }

// specialists maps a sub-agent name to the offline answer it gives.
var specialists = map[string]string{
	"billing_agent": "Refund of the duplicate charge opened as case BIL-7.",
	"tech_agent":    "Known issue: clear the app cache and update to 5.2.",
}

// coordinatorBrain decides the delegation — the step C1 pays a full model
// call for on every hop. Once a sub-agent answered, it summarises.
func coordinatorBrain(p kit.Prompt) kit.Reply {
	for name := range specialists {
		if r, ok := p.Result(name); ok {
			return kit.Say("Handled by %s: %v", name, r["result"])
		}
	}
	switch {
	case p.Has("charge", "invoice", "refund", "billing", "payment"):
		return kit.Call("billing_agent", map[string]any{"request": p.Text})
	case p.Has("crash", "error", "bug", "login", "slow"):
		return kit.Call("tech_agent", map[string]any{"request": p.Text})
	default:
		return kit.Say("I can help with billing or technical issues. Which one is it?")
	}
}

func specialistBrain(name string) kit.Brain {
	return func(kit.Prompt) kit.Reply { return kit.Say("%s", specialists[name]) }
}

func specialist(m kit.Models, name, desc string) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:        name,
		Description: desc,
		Model:       m.For(name, specialistBrain(name)),
		Instruction: fmt.Sprintf("You are the %s. Resolve the request in one or two sentences.", desc),
		// ModeSingleTurn: reached as a function call, runs in its own branch,
		// returns one result and never chats with the user.
		Mode: llmagent.ModeSingleTurn,
	})
}

func build(m kit.Models) (agent.Agent, error) {
	billing, err := specialist(m, "billing_agent", "billing specialist: charges, refunds, invoices")
	if err != nil {
		return nil, err
	}
	tech, err := specialist(m, "tech_agent", "technical support specialist: crashes, errors, login problems")
	if err != nil {
		return nil, err
	}
	return llmagent.New(llmagent.Config{
		Name:        "coordinator",
		Description: "Routes customer requests to specialists.",
		Model:       m.For("coordinator", coordinatorBrain),
		Instruction: `You are a support coordinator. Delegate billing questions to billing_agent
and technical problems to tech_agent by calling them with the user's request.
Then give the user one sentence naming who handled it and the result.`,
		SubAgents: []agent.Agent{billing, tech},
	})
}
