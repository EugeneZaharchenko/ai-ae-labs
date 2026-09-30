// A2 · ReAct — thought → action → observation in a loop until an exit
// condition fires. ADK has no "ReAct" type: it is a shape. Here it is a
// dynamic node whose body is a plain Go for-loop with a hard step cap that
// WE write — no construct in the framework caps it for us.
//
//	Start → react (DynamicNode)
//	          loop ≤ maxSteps:
//	            planner (LlmAgent)  → "SEARCH: q" | "FINAL: answer"
//	            search  (FunctionNode) → observation
//
//	go run .                 # scripted demo: empty result → re-plan → answer
//	go run . -live
//	go run . console
package main

import (
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:    "A2",
	Title: "ReAct",
	Input: "When was ADK Go 2.0 announced?",
	Build: build,
}

func main() { kit.Main(spec) }

// maxSteps is the hard cap. Without it a ReAct loop wanders.
const maxSteps = 4

const noResults = "(no results)"

// result is the react node output: the answer and what it cost.
type result struct {
	Answer string `json:"answer"`
	Steps  int    `json:"steps"`
	Capped bool   `json:"capped,omitempty"`
}

// knowledge is the offline search index. It only matches precise queries,
// which is what makes the first, naive query come back empty.
var knowledge = map[string]string{
	"release": "ADK Go 2.0 was announced in 2026 with a graph workflow engine.",
	"graph":   "ADK 2.x executes agents, tools and functions as graph nodes.",
}

func search(_ agent.Context, query string) (string, error) {
	low := strings.ToLower(query)
	for key, doc := range knowledge {
		if strings.Contains(low, key) {
			return doc, nil
		}
	}
	return noResults, nil
}

// plannerBrain is the offline planner: ask the question as-is, and when the
// observation is empty, re-plan with a sharper query. It answers once it has
// a non-empty observation.
func plannerBrain(p kit.Prompt) kit.Reply {
	obs := lastObservation(p.Text)
	switch {
	case obs == "":
		return kit.Say("SEARCH: %s", question(p.Text))
	case obs == noResults:
		return kit.Say("SEARCH: %s release date", strings.TrimSuffix(question(p.Text), "?"))
	default:
		return kit.Say("FINAL: %s", obs)
	}
}

// scratchpad is the planner's whole context each step: the question and
// every action/observation so far. A single-turn agent node sees only its
// input, so the loop — not session history — carries the trace.
func scratchpad(q string, steps []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Question: %s\n", q)
	for _, s := range steps {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func question(pad string) string {
	first, _, _ := strings.Cut(pad, "\n")
	return strings.TrimSpace(strings.TrimPrefix(first, "Question:"))
}

func lastObservation(pad string) string {
	lines := strings.Split(pad, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if o, ok := strings.CutPrefix(lines[i], "Observation: "); ok {
			return strings.TrimSpace(o)
		}
	}
	return ""
}

// react is the loop. Every child run is a durable, cached activation, so a
// resumed run skips the steps already taken.
func react(planner, searcher workflow.Node) workflow.DynamicFn[string, result] {
	return func(ctx agent.Context, q string, _ func(*session.Event) error) (result, error) {
		var steps []string
		for step := 1; step <= maxSteps; step++ {
			thought, err := workflow.RunNode[string](ctx, planner, scratchpad(q, steps))
			if err != nil {
				return result{}, fmt.Errorf("step %d think: %w", step, err)
			}
			thought = strings.TrimSpace(thought)
			if answer, ok := strings.CutPrefix(thought, "FINAL:"); ok {
				return result{Answer: strings.TrimSpace(answer), Steps: step}, nil
			}
			query, ok := strings.CutPrefix(thought, "SEARCH:")
			if !ok {
				// The planner broke the protocol: record it and let it retry.
				steps = append(steps, "Action: none", "Observation: reply must start with SEARCH: or FINAL:")
				continue
			}
			obs, err := workflow.RunNode[string](ctx, searcher, strings.TrimSpace(query))
			if err != nil {
				return result{}, fmt.Errorf("step %d act: %w", step, err)
			}
			steps = append(steps, "Action: search "+strings.TrimSpace(query), "Observation: "+obs)
		}
		return result{Answer: "no answer within the step budget", Steps: maxSteps, Capped: true}, nil
	}
}

func build(m kit.Models) (agent.Agent, error) {
	return newReact(m.For("planner", plannerBrain))
}

// newReact wires the loop around any planner model; tests swap in planners
// that never finish or break the protocol.
func newReact(plannerModel model.LLM) (agent.Agent, error) {
	plannerAgent, err := llmagent.New(llmagent.Config{
		Name:        "planner",
		Description: "Decides the next ReAct step.",
		Model:       plannerModel,
		Instruction: `You answer the Question with a search tool, one step at a time.
Read the Action/Observation lines. Reply with exactly one line:
"SEARCH: <query>" to search, or "FINAL: <answer>" once an observation answers the question.
If the last observation is "(no results)", search again with a different, more specific query.`,
	})
	if err != nil {
		return nil, err
	}
	planner, err := workflow.NewAgentNode(plannerAgent, workflow.NodeConfig{})
	if err != nil {
		return nil, err
	}
	searcher := workflow.NewFunctionNode("search", search, workflow.NodeConfig{})
	loop := workflow.NewDynamicNode("react", react(planner, searcher), workflow.NodeConfig{})

	return workflowagent.New(workflowagent.Config{
		Name:        "react_agent",
		Description: "ReAct loop with a hard step cap.",
		Edges:       workflow.Chain(workflow.Start, loop),
	})
}
