// Package adkrun is the headless harness that every course-lab CLI
// uses to execute its ADK workflow agent: in-memory session, one user
// message in, event stream out, final node output returned as text.
//
// Verified against google.golang.org/adk/v2 v2.0.0 (станом на 07/2026).
package adkrun

import (
	"context"
	"fmt"
	"io"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"
)

// NodeConfig returns the default retry-enabled node configuration.
func NodeConfig() workflow.NodeConfig {
	return workflow.NodeConfig{RetryConfig: workflow.DefaultRetryConfig()}
}

// Pipeline declares a linear workflow agent from ordered nodes.
func Pipeline(name, description string, nodes ...workflow.Node) (agent.Agent, error) {
	all := append([]workflow.Node{workflow.Start}, nodes...)
	return workflowagent.New(workflowagent.Config{
		Name:        name,
		Description: description,
		Edges:       workflow.Chain(all...),
	})
}

// Run executes a (workflow) agent headlessly: it sends input as the
// user message, mirrors intermediate event text to progress, and
// returns the text of the final event.
func Run(ctx context.Context, a agent.Agent, input string, progress io.Writer) (string, error) {
	r, err := runner.New(runner.Config{
		AppName:           "course-lab",
		Agent:             a,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return "", fmt.Errorf("adkrun: %w", err)
	}
	msg := genai.NewContentFromText(input, genai.RoleUser)

	var final string
	for event, runErr := range r.Run(ctx, "course-lab", "run", msg, agent.RunConfig{}) {
		if runErr != nil {
			return final, fmt.Errorf("adkrun: %s: %w", a.Name(), runErr)
		}
		text := eventText(event)
		if text == "" {
			continue
		}
		final = text
		if progress != nil {
			fmt.Fprintf(progress, "· %s\n", firstLine(text))
		}
	}
	return final, nil
}

func eventText(e *session.Event) string {
	if e == nil {
		return ""
	}
	if e.LLMResponse.Content != nil {
		var b strings.Builder
		for _, p := range e.LLMResponse.Content.Parts {
			if p != nil && p.Text != "" {
				b.WriteString(p.Text)
			}
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			return s
		}
	}
	if s, ok := e.Output.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
