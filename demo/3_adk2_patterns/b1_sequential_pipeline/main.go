// B1 · Sequential Pipeline — a fixed order of steps; each step's output is
// the next step's input. Code decides the order, so no model call is spent
// rediscovering it.
//
//	Start → extract (fn) → clean (fn) → summarize (LlmAgent) → load (fn)
//
//	go run .
//	go run . -input "From: bob@x.io  Ticket: refund   for order ORD-7 please"
//	go run . -live
package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:    "B1",
	Title: "Sequential Pipeline",
	Input: "From: anna@example.com   Ticket:   my card was charged TWICE for order ORD-42.   Please refund one charge.",
	Build: build,
}

func main() { kit.Main(spec) }

// ticket is the record that flows through the pipeline.
type ticket struct {
	Sender string `json:"sender"`
	Body   string `json:"body"`
}

var emailRE = regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.]+`)

// extract splits the raw message into fields.
func extract(_ agent.Context, raw string) (ticket, error) {
	t := ticket{Sender: emailRE.FindString(raw), Body: raw}
	if _, body, ok := strings.Cut(raw, "Ticket:"); ok {
		t.Body = body
	}
	if strings.TrimSpace(t.Body) == "" {
		return ticket{}, fmt.Errorf("extract: empty ticket body")
	}
	return t, nil
}

// clean normalises whitespace and masks PII before any model sees the text.
func clean(_ agent.Context, t ticket) (ticket, error) {
	t.Body = strings.Join(strings.Fields(emailRE.ReplaceAllString(t.Body, "[email]")), " ")
	if t.Sender != "" {
		t.Sender = "[email]"
	}
	return t, nil
}

// summarizeBrain is the offline summariser: it reads the JSON ticket the
// agent node received and returns its first sentence.
func summarizeBrain(p kit.Prompt) kit.Reply {
	var t ticket
	if err := json.Unmarshal([]byte(p.Text), &t); err != nil || t.Body == "" {
		return kit.Say("SUMMARY: %s", p.Text)
	}
	first, _, _ := strings.Cut(t.Body, ".")
	return kit.Say("SUMMARY: %s.", strings.TrimSpace(first))
}

// load is the sink: in a real system a DB write, here a formatted line.
func load(_ agent.Context, summary string) (string, error) {
	return "loaded → " + strings.TrimSpace(summary), nil
}

func build(m kit.Models) (agent.Agent, error) {
	summarizer, err := llmagent.New(llmagent.Config{
		Name:        "summarize",
		Description: "Summarises one cleaned ticket.",
		Model:       m.For("summarize", summarizeBrain),
		Instruction: `You receive one support ticket as JSON with fields sender and body.
Reply with one line: "SUMMARY: <one sentence describing the customer's problem>".`,
	})
	if err != nil {
		return nil, err
	}
	summarize, err := workflow.NewAgentNode(summarizer, workflow.NodeConfig{})
	if err != nil {
		return nil, err
	}
	return workflowagent.New(workflowagent.Config{
		Name:        "ticket_pipeline",
		Description: "extract → clean → summarize → load",
		Edges: workflow.Chain(
			workflow.Start,
			workflow.NewFunctionNode("extract", extract, workflow.NodeConfig{}),
			workflow.NewFunctionNode("clean", clean, workflow.NodeConfig{}),
			summarize,
			workflow.NewFunctionNode("load", load, workflow.NodeConfig{}),
		),
	})
}
