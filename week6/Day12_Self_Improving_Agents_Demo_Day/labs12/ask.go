package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
)

// runAgent runs an agent once with a fresh in-memory session.
func runAgent(ctx context.Context, a agent.Agent, input string) (labrun.Result, error) {
	return labrun.Run(ctx, a, input)
}

// askSession runs one question with a real human pause: the graph stops at
// approval_gate, the human answers within the window, the answer goes to the
// ledger FIRST, and only then the run is resumed.
type askSession struct {
	runner *runner.Runner
	user   string
	sid    string
}

func newAskSession(a agent.Agent) (*askSession, error) {
	r, err := runner.New(runner.Config{
		AppName: AppName, Agent: a, SessionService: session.InMemoryService(), AutoCreateSession: true,
	})
	if err != nil {
		return nil, fmt.Errorf("runner: %w", err)
	}
	return &askSession{runner: r, user: "researcher", sid: "ask-" + time.Now().Format("20060102-150405")}, nil
}

func (s *askSession) turn(ctx context.Context, msg *genai.Content) ([]*session.Event, error) {
	var events []*session.Event
	for ev, err := range s.runner.Run(ctx, s.user, s.sid, msg, agent.RunConfig{}) {
		if err != nil {
			return events, err
		}
		events = append(events, ev)
	}
	return events, nil
}

// pauseOf finds the HITL request in a run: its interrupt id and payload.
func pauseOf(events []*session.Event) (string, map[string]any, bool) {
	for _, ev := range events {
		if ev.LLMResponse.Content == nil {
			continue
		}
		for _, p := range ev.LLMResponse.Content.Parts {
			if p != nil && p.FunctionCall != nil && p.FunctionCall.Name == workflow.WorkflowInputFunctionCallName {
				payload, _ := p.FunctionCall.Args["payload"].(map[string]any)
				return p.FunctionCall.ID, payload, true
			}
		}
	}
	return "", nil, false
}

// resumeMessage is the reply that wakes the paused gate up.
func resumeMessage(id, word string) *genai.Content {
	return &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{
		FunctionResponse: &genai.FunctionResponse{
			ID: id, Name: workflow.WorkflowInputFunctionCallName, Response: map[string]any{"payload": word},
		},
	}}}
}

func ask(ctx context.Context, o options, root string, models Models, lib *Library, in io.Reader, out io.Writer) error {
	s, err := loadStrategy(root)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, "approvals.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("approvals.log: %w", err)
	}
	defer f.Close()
	ledger := NewLedger(f)
	meter := NewMeter()
	pipe, err := NewPipeline(PipelineConfig{
		Strategy: s, Lib: lib, Writer: models.Writer, Critic: models.Critic,
		Gate: &Gate{Mode: GateHuman, Ledger: ledger}, Meter: meter,
	})
	if err != nil {
		return err
	}
	sess, err := newAskSession(pipe)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "стратегія: %s\n\n", s.Title)
	events, err := sess.turn(ctx, genai.NewContentFromText(o.ask, genai.RoleUser))
	printTrace(out, events)
	if err != nil {
		return err
	}
	id, payload, paused := pauseOf(events)
	if !paused {
		return fmt.Errorf("конвеєр не дійшов до approval_gate")
	}
	fmt.Fprintf(out, "\n── ПАУЗА %s: потрібне рішення людини ──\n%s\n", id, payload["report"])
	fmt.Fprintf(out, "\nдокази: хеш %v; критики пройшли: %v (%v); цитовані джерела: %v; джерела доказів: %v\n",
		payload["report_hash"], payload["critics_passed"], payload["verdicts"], payload["cited_sources"], payload["evidence_from"])
	fmt.Fprintf(out, "approve / deny (чекаю %s; тиша = відмова)> ", o.window)

	word, rerr := readLine(ctx, in, o.window)
	status, by, reason := StatusDenied, o.by, "людина відмовила"
	switch st, perr := ParseDecision(word); {
	case rerr != nil:
		status, by, reason = StatusTimedOut, "system:timeout", rerr.Error()
	case perr != nil:
		status, reason = StatusInvalid, perr.Error()
	case st == StatusApproved:
		status, reason = StatusApproved, "людина схвалила"
	}
	if _, err := ledger.Decide(id, status, by, reason); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nжурнал: %s → %s (by %s)\n", id, status, by)
	events, err = sess.turn(ctx, resumeMessage(id, word))
	printTrace(out, events)
	if err != nil {
		return err
	}
	for _, ev := range events {
		if t, ok := ev.Output.(string); ok && t != "" {
			fmt.Fprintf(out, "\n══ ФІНАЛЬНИЙ ЗВІТ ══\n%s\n", t)
		}
	}
	fmt.Fprintf(out, "\nтокени (UsageMetadata): %d\n", meter.Total())
	return nil
}

// printTrace prints the pipeline's own event-log lines (w6p2:*).
func printTrace(out io.Writer, events []*session.Event) {
	for _, ev := range events {
		keys := make([]string, 0, len(ev.Actions.StateDelta))
		for k := range ev.Actions.StateDelta {
			if strings.HasPrefix(k, "w6p2:") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(out, "  %-24s %v\n", strings.TrimPrefix(k, "w6p2:"), ev.Actions.StateDelta[k])
		}
	}
}
