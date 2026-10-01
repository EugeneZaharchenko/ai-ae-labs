// Package labrun is the headless harness every lab solution uses to execute an
// agent in a test: in-memory session, one user message in, the full event
// stream out.
//
// Why this exists: without it, each lab would either (a) need a real provider
// key to demonstrate anything, or (b) test its pure logic only and never prove
// the agent is actually wired up. labrun plus internal/fakellm makes the whole
// agent path — instruction, tool declaration, tool dispatch, final answer —
// assertable offline and deterministically.
//
// Verified against google.golang.org/adk/v2 v2.5.0 (станом на 09/2026).
package labrun

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// Result is everything a test needs to assert about one agent run.
type Result struct {
	// Final is the text of the last event that carried any.
	Final string
	// Events is every event the runner emitted, in order. This is the
	// event-sourced trace the week 5 lecture talks about.
	Events []*session.Event
	// ToolCalls names every function call the model requested, in order.
	ToolCalls []string
	// ToolResults holds every function response, in order.
	ToolResults []*genai.FunctionResponse
}

// Texts returns the non-empty text of each event, in order.
func (r Result) Texts() []string {
	out := make([]string, 0, len(r.Events))
	for _, e := range r.Events {
		if t := EventText(e); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// CalledTool reports whether the model requested the named tool at least once.
func (r Result) CalledTool(name string) bool {
	for _, got := range r.ToolCalls {
		if got == name {
			return true
		}
	}
	return false
}

// Run executes an agent headlessly with a fresh in-memory session.
//
// It always drains the event stream: a partial run still returns the events
// collected so far alongside the error, because "which step failed" is the
// question a lab learner actually has.
func Run(ctx context.Context, a agent.Agent, input string) (Result, error) {
	return RunWithSession(ctx, a, "lab-user", "lab-session", input)
}

// RunWithSession is Run with explicit user and session IDs, for labs that need
// two runs to share (or deliberately not share) session state.
func RunWithSession(ctx context.Context, a agent.Agent, userID, sessionID, input string) (Result, error) {
	svc := session.InMemoryService()
	return runOn(ctx, a, svc, userID, sessionID, input)
}

// Runner builds a reusable runner over one session service, so a lab can make
// several turns against the SAME session and observe accumulated state.
type Runner struct {
	agent   agent.Agent
	service session.Service
}

// NewRunner returns a Runner backed by a single in-memory session service.
func NewRunner(a agent.Agent) *Runner {
	return &Runner{agent: a, service: session.InMemoryService()}
}

// Service exposes the underlying session service so a test can inspect stored
// state directly after a turn.
func (r *Runner) Service() session.Service { return r.service }

// Turn runs one more message against the same session.
func (r *Runner) Turn(ctx context.Context, userID, sessionID, input string) (Result, error) {
	return runOn(ctx, r.agent, r.service, userID, sessionID, input)
}

func runOn(ctx context.Context, a agent.Agent, svc session.Service, userID, sessionID, input string) (Result, error) {
	run, err := runner.New(runner.Config{
		AppName:           "labs",
		Agent:             a,
		SessionService:    svc,
		AutoCreateSession: true,
	})
	if err != nil {
		return Result{}, fmt.Errorf("labrun: build runner: %w", err)
	}

	msg := genai.NewContentFromText(input, genai.RoleUser)

	var res Result
	for event, runErr := range run.Run(ctx, userID, sessionID, msg, agent.RunConfig{}) {
		if runErr != nil {
			return res, fmt.Errorf("labrun: run %s: %w", a.Name(), runErr)
		}
		res.Events = append(res.Events, event)
		collectCalls(&res, event)
		if t := EventText(event); t != "" {
			res.Final = t
		}
	}
	return res, nil
}

func collectCalls(res *Result, e *session.Event) {
	if e == nil || e.LLMResponse.Content == nil {
		return
	}
	for _, p := range e.LLMResponse.Content.Parts {
		if p == nil {
			continue
		}
		if p.FunctionCall != nil {
			res.ToolCalls = append(res.ToolCalls, p.FunctionCall.Name)
		}
		if p.FunctionResponse != nil {
			res.ToolResults = append(res.ToolResults, p.FunctionResponse)
		}
	}
}

// EventText extracts the human-readable text of an event.
//
// Workflow function nodes put their output in Event.Output; LLM and agent
// events carry it in Content parts. A lab harness has to read both, because
// week 2 mixes the two in one graph.
func EventText(e *session.Event) string {
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
