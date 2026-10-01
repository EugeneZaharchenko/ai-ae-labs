// The three prebuilt workflow agents of ADK Go v2.5.0:
// sequentialagent, parallelagent and loopagent.
//
// These are the "prebuilt" workflow style — the third of the three ways ADK
// composes multi-step work, next to graph-based and dynamic workflows. They
// need no graph assembly: you hand each one a list of sub-agents and it runs
// them. Reach for them when your shape is exactly sequence / parallel / loop,
// and switch to a graph the moment you need routing, fan-in, retries or HITL.
//
// Import path note: the plural directory `agent/workflowagents/` holds these
// three. It is NOT `agent/workflowagent` (singular), which adapts a
// workflow.Workflow graph into an agent.Agent.
//
// Verified against v2.5.0: all three run offline with a rule-based model.
package examples

import (
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagents/loopagent"
	"google.golang.org/adk/v2/agent/workflowagents/parallelagent"
	"google.golang.org/adk/v2/agent/workflowagents/sequentialagent"
	"google.golang.org/adk/v2/model"
)

// ModelFor picks the model for one named stage. It mirrors the shape of the
// offline `kit.Models.For(name, brain)`, so a per-agent rule-based model slots
// straight in and keeps every prebuilt agent deterministic in tests.
type ModelFor func(name string) model.LLM

// NewSequentialPipeline runs its sub-agents once each, in list order. The last
// sub-agent's text is the result. Use it for a fixed, strict order — the graph
// equivalent is B1's `workflow.Chain`.
func NewSequentialPipeline(m ModelFor, stages ...string) (agent.Agent, error) {
	subs, err := leaves(m, stages...)
	if err != nil {
		return nil, err
	}
	return sequentialagent.New(sequentialagent.Config{
		AgentConfig: agent.Config{
			Name:        "pipeline",
			Description: "Runs its stages once each, in order.",
			SubAgents:   subs,
		},
	})
}

// NewParallelResearch runs its sub-agents concurrently, each in an isolated
// branch. There is NO fan-in: branches cannot see each other's state, and
// results arrive in completion order, not list order. If you need to combine
// what they produced, follow this with a merging agent — or use the graph form
// (B2's `AddFanOut` + `NewJoinNode`) instead, which joins by construction.
func NewParallelResearch(m ModelFor, researchers ...string) (agent.Agent, error) {
	subs, err := leaves(m, researchers...)
	if err != nil {
		return nil, err
	}
	return parallelagent.New(parallelagent.Config{
		AgentConfig: agent.Config{
			Name:        "parallel_research",
			Description: "Runs its sub-agents concurrently in isolated branches.",
			SubAgents:   subs,
		},
	})
}

// NewRefinementLoop repeats its sub-agent list up to maxIterations times.
//
// Two things the prebuilt loop does not do for you:
//
//  1. maxIterations == 0 means loop FOREVER — it is not "skip the loop". The
//     loop then exits only when a sub-agent escalates. Always pass a real cap.
//  2. Nothing checks your exit condition. The loop re-runs the whole list until
//     the cap or an escalation, so the exit test must live inside a sub-agent.
//
// Pass maxIterations >= 1. The graph equivalent (B3) uses a routed back edge.
func NewRefinementLoop(m ModelFor, maxIterations uint, stages ...string) (agent.Agent, error) {
	subs, err := leaves(m, stages...)
	if err != nil {
		return nil, err
	}
	return loopagent.New(loopagent.Config{
		AgentConfig: agent.Config{
			Name:        "refinement_loop",
			Description: "Repeats its stages until the cap or an escalation.",
			SubAgents:   subs,
		},
		MaxIterations: maxIterations,
	})
}

// EscalateFromToolBody is how a sub-agent ends a prebuilt loop early.
//
// The loop reacts only to the flag on an event the sub-agent emits, so a tool
// reached inside the loop must set it on the live context — returning a value
// does nothing. With no escalation the loop runs to its cap and no further.
func EscalateFromToolBody(ctx agent.Context) string {
	ctx.Actions().Escalate = true
	return "done"
}

// leaves builds one plain agent per name, each with its own model.
func leaves(m ModelFor, names ...string) ([]agent.Agent, error) {
	out := make([]agent.Agent, 0, len(names))
	for _, name := range names {
		a, err := llmagent.New(llmagent.Config{
			Name:        name,
			Description: "Answers the request in one sentence.",
			Model:       m(name),
			Instruction: "Answer the request in one sentence.",
		})
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}
