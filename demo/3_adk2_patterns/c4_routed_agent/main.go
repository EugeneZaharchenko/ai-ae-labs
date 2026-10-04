// C4 · Routed Agent — your own routing function picks exactly one agent per
// call (model tiering), with failover to the other tier if the chosen agent
// fails before producing anything.
//
// ADK Go v2.5.0 has no RoutedAgent / AgentRouter (those are TypeScript, per
// the catalog's source). This is the Go construction: a dynamic node holds the
// routing rule and the failover in plain Go, and runs the agents with RunNode.
//
//	router (DynamicNode) ─ rule ─→ cheap_agent  ─✗ error → strong_agent
//	                            └→ strong_agent ─✗ error → cheap_agent
//
//	go run .                                     # outage on cheap tier → failover
//	go run . -input "What is my data limit?"     # cheap tier, no failover
//	go run . console
package main

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:    "C4",
	Title: "Routed Agent",
	Input: "What is my data limit? (simulate outage)",
	Build: build,
}

func main() { kit.Main(spec) }

// errOutage is the simulated provider failure of the offline demo.
var errOutage = errors.New("503 provider unavailable (simulated)")

// tier is the deterministic routing rule: long or analytical requests go to
// the strong model, everything else to the cheap one. It is code, not a model
// call — that is what separates C4 from C1.
func tier(input string) string {
	low := strings.ToLower(input)
	if len(input) > 120 || strings.Contains(low, "analy") || strings.Contains(low, "compare") {
		return "strong_agent"
	}
	return "cheap_agent"
}

func tierBrain(name string) kit.Brain {
	return func(p kit.Prompt) kit.Reply {
		// The outage hits whichever tier the router picked first; the demo
		// input asks for it explicitly.
		if p.Has("simulate outage") && name == tier(p.Text) {
			return kit.Reply{Err: errOutage}
		}
		return kit.Say("[%s] answer to: %s", name, p.Text)
	}
}

// result records which agent answered and whether failover happened.
type result struct {
	Answer   string `json:"answer"`
	Agent    string `json:"agent"`
	Failover bool   `json:"failover"`
}

func route(agents map[string]workflow.Node) workflow.DynamicFn[string, result] {
	return func(ctx agent.Context, in string, _ func(*session.Event) error) (result, error) {
		primary := tier(in)
		fallback := "strong_agent"
		if primary == fallback {
			fallback = "cheap_agent"
		}
		out, err := workflow.RunNode[any](ctx, agents[primary], in)
		if err == nil {
			return result{Answer: fmt.Sprint(out), Agent: primary}, nil
		}
		// Failover is only safe because a model error happens before the
		// agent emitted any output: nothing partial reached the user.
		out, ferr := workflow.RunNode[any](ctx, agents[fallback], in)
		if ferr != nil {
			return result{}, fmt.Errorf("both tiers failed: %s: %w; %s: %w", primary, err, fallback, ferr)
		}
		return result{Answer: fmt.Sprint(out), Agent: fallback, Failover: true}, nil
	}
}

func build(m kit.Models) (agent.Agent, error) { return buildWith(m, tierBrain) }

// buildWith takes the offline brain per tier, so a test can take both down.
func buildWith(m kit.Models, brainFor func(string) kit.Brain) (agent.Agent, error) {
	agents := map[string]workflow.Node{}
	for name, instr := range map[string]string{
		"cheap_agent":  "Answer short factual questions in one sentence.",
		"strong_agent": "Answer analytical or long questions thoroughly but concisely.",
	} {
		a, err := llmagent.New(llmagent.Config{
			Name:        name,
			Description: instr,
			Model:       m.For(name, brainFor(name)),
			Instruction: instr,
		})
		if err != nil {
			return nil, err
		}
		n, err := workflow.NewAgentNode(a, workflow.NodeConfig{})
		if err != nil {
			return nil, err
		}
		agents[name] = n
	}
	return workflowagent.New(workflowagent.Config{
		Name:        "routed_agent",
		Description: "Model tiering with failover.",
		Edges:       workflow.Chain(workflow.Start, workflow.NewDynamicNode("router", route(agents), workflow.NodeConfig{})),
	})
}
