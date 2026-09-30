// B4 · Conditional Route — one cheap classifier, then code dispatches to
// exactly one typed branch. A Default branch catches everything else.
//
//	Start → classify (LlmAgent) → dispatch ─BUG──────→ bug_desk
//	                                        ├SUPPORT──→ support_desk
//	                                        └Default──→ human_triage
//
//	go run .                      # scripted demo, offline
//	go run . -input "invoice?"    # watch it fall through to Default
//	go run . console
package main

import (
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
	ID:    "B4",
	Title: "Conditional Route",
	Input: "The app crashes with an error when I upload a photo",
	Build: build,
}

func main() { kit.Main(spec) }

// labels are the enumerable categories — the precondition for B4.
var labels = []string{"BUG", "SUPPORT"}

func classifierBrain(p kit.Prompt) kit.Reply {
	switch {
	case p.Has("crash", "error", "bug", "broken", "exception"):
		return kit.Say("BUG")
	case p.Has("password", "login", "how do i", "account"):
		return kit.Say("SUPPORT")
	default:
		return kit.Say("OTHER")
	}
}

// label normalises whatever the classifier said to one known label, or "".
func label(text string) string {
	up := strings.ToUpper(strings.TrimSpace(text))
	for _, l := range labels {
		if strings.Contains(up, l) {
			return l
		}
	}
	return ""
}

// dispatch is the deterministic half: it turns the label into a route.
// An unknown label emits no route, so only the Default edge matches.
func dispatch(ctx agent.Context, in any) (*session.Event, error) {
	text := fmt.Sprint(in)
	ev := session.NewEvent(ctx, ctx.InvocationID())
	ev.Output = text
	if l := label(text); l != "" {
		ev.Routes = []string{l}
	}
	return ev, nil
}

func desk(name, reply string) workflow.Node {
	return workflow.NewFunctionNode(name, func(_ agent.Context, in any) (string, error) {
		return fmt.Sprintf("%s: %s", name, reply), nil
	}, workflow.NodeConfig{})
}

func build(m kit.Models) (agent.Agent, error) {
	classifier, err := llmagent.New(llmagent.Config{
		Name:        "classify",
		Description: "Labels a ticket.",
		Model:       m.For("classify", classifierBrain),
		Instruction: "Classify the ticket. Answer with exactly one word: BUG, SUPPORT or OTHER.",
	})
	if err != nil {
		return nil, err
	}
	classify, err := workflow.NewAgentNode(classifier, workflow.NodeConfig{})
	if err != nil {
		return nil, err
	}
	router := workflow.NewFunctionNode("dispatch", dispatch, workflow.NodeConfig{})

	edges := workflow.NewEdgeBuilder().
		Add(workflow.Start, classify).
		Add(classify, router).
		AddRoutes(router, map[string]workflow.Node{
			"BUG":     desk("bug_desk", "ticket filed for engineering"),
			"SUPPORT": desk("support_desk", "sent a how-to article"),
		}).
		// Always define Default: an unclassified ticket must go somewhere.
		AddRoute(router, desk("human_triage", "queued for a human"), workflow.Default).
		Build()

	return workflowagent.New(workflowagent.Config{
		Name:        "triage",
		Description: "Classifier + deterministic dispatch.",
		Edges:       edges,
	})
}
