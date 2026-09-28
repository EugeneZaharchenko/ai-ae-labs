// D1 · Review and Critique — a generator writes, a separate critic in its own
// context checks the result against enumerated criteria and returns a
// structured verdict; code routes on that verdict.
//
//	Start → generate (LlmAgent) → critic (LlmAgent) → gate ─true──→ publish
//	                                                        └false─→ send_back
//
//	go run .                                           # approve path, offline
//	go run . -input "Promise ORD-42 arrives tomorrow"   # reject path
//	go run . console
package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:    "D1",
	Title: "Review and Critique",
	Input: "Tell the customer that order ORD-42 is delayed",
	Build: build,
}

func main() { kit.Main(spec) }

// criteria are the enumerated checks. "Review this" breeds sycophancy; a list
// of named, checkable criteria does not.
var criteria = []string{
	"C1: names the order id (ORD-<digits>)",
	"C2: at most 30 words",
	"C3: makes no promise it cannot keep (guarantee, promise, tomorrow)",
}

// verdict is the critic's structured output.
type verdict struct {
	Approve bool     `json:"approve"`
	Failed  []string `json:"failed,omitempty"`
}

var orderRe = regexp.MustCompile(`ORD-\d+`)

// review applies the criteria deterministically. The offline critic uses it;
// a live critic is asked to apply the same list.
func review(draft string) verdict {
	var failed []string
	if !orderRe.MatchString(draft) {
		failed = append(failed, "C1")
	}
	if len(strings.Fields(draft)) > 30 {
		failed = append(failed, "C2")
	}
	low := strings.ToLower(draft)
	for _, w := range []string{"guarantee", "promise", "tomorrow"} {
		if strings.Contains(low, w) {
			failed = append(failed, "C3")
			break
		}
	}
	return verdict{Approve: len(failed) == 0, Failed: failed}
}

func generatorBrain(p kit.Prompt) kit.Reply {
	id := orderRe.FindString(strings.ToUpper(p.Text))
	if p.Has("promise", "guarantee", "tomorrow") {
		// The generator's momentum carries the customer's wish into the draft.
		return kit.Say("We guarantee order %s arrives tomorrow, promise!", id)
	}
	if id == "" {
		return kit.Say("Sorry, your order is delayed; we will send a new ETA soon.")
	}
	return kit.Say("Sorry, order %s is delayed; the new ETA is 2026-10-01.", id)
}

func criticBrain(p kit.Prompt) kit.Reply {
	b, _ := json.Marshal(review(p.Text))
	return kit.Say("%s", b)
}

// parseVerdict reads the critic's JSON; anything unparseable is a reject,
// never a silent approve.
func parseVerdict(text string) verdict {
	text = strings.TrimSpace(text)
	if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		text = text[i : j+1]
	}
	var v verdict
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return verdict{Failed: []string{"unparseable verdict"}}
	}
	return v
}

// gate turns the verdict into a BoolRoute ("true"/"false").
func gate(ctx agent.Context, in any) (*session.Event, error) {
	v := parseVerdict(fmt.Sprint(in))
	ev := session.NewEvent(ctx, ctx.InvocationID())
	ev.Output = v
	ev.Routes = []string{fmt.Sprint(workflow.BoolRoute(v.Approve))}
	return ev, nil
}

func draft(ctx agent.Context) string {
	d, err := ctx.State().Get("draft")
	if err != nil {
		return ""
	}
	return fmt.Sprint(d)
}

func publish(ctx agent.Context, _ verdict) (string, error) {
	return "published: " + draft(ctx), nil
}

func sendBack(ctx agent.Context, v verdict) (string, error) {
	return fmt.Sprintf("sent back to the writer, failed %s: %s", strings.Join(v.Failed, ","), draft(ctx)), nil
}

func build(m kit.Models) (agent.Agent, error) {
	generator, err := llmagent.New(llmagent.Config{
		Name:        "generate",
		Description: "Drafts a customer reply.",
		Model:       m.For("generate", generatorBrain),
		Instruction: "Draft a short reply to the customer about their order. Reply with the draft only.",
		OutputKey:   "draft",
	})
	if err != nil {
		return nil, err
	}
	critic, err := llmagent.New(llmagent.Config{
		Name:        "critic",
		Description: "Checks a draft against enumerated criteria.",
		Model:       m.For("critic", criticBrain),
		Instruction: "You review a customer reply draft. Check EVERY criterion:\n" +
			strings.Join(criteria, "\n") +
			"\nAnswer ONLY with JSON: {\"approve\": bool, \"failed\": [\"C1\", ...]}.",
	})
	if err != nil {
		return nil, err
	}
	genNode, err := workflow.NewAgentNode(generator, workflow.NodeConfig{})
	if err != nil {
		return nil, err
	}
	criticNode, err := workflow.NewAgentNode(critic, workflow.NodeConfig{})
	if err != nil {
		return nil, err
	}
	gateNode := workflow.NewFunctionNode("gate", gate, workflow.NodeConfig{})

	edges := workflow.NewEdgeBuilder().
		Add(workflow.Start, genNode).
		Add(genNode, criticNode).
		Add(criticNode, gateNode).
		AddRoute(gateNode, workflow.NewFunctionNode("publish", publish, workflow.NodeConfig{}), workflow.BoolRoute(true)).
		AddRoute(gateNode, workflow.NewFunctionNode("send_back", sendBack, workflow.NodeConfig{}), workflow.BoolRoute(false)).
		Build()

	return workflowagent.New(workflowagent.Config{
		Name:        "reply_review",
		Description: "Generator + separate critic + verdict gate.",
		Edges:       edges,
	})
}
