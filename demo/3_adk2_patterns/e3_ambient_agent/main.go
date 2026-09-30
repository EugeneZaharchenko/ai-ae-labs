// E3 · Ambient Agent — no human starts the run: an event does. This is a
// deployment wrapper, not an orchestration pattern; the body can be any
// pattern A–D (here a small B4-style triage graph).
//
//	queue (chan ticket) → worker pool → fresh session per event → body agent → outcome
//	                                                                            ↓
//	                                              summary that names every failure's reason
//
//	go run .            # drain a simulated queue of 4 tickets, offline
//	go run . console    # talk to the body agent directly
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:    "E3",
	Title: "Ambient Agent",
	Input: "Checkout page returns 500 for every customer",
	Build: build,
	Demo:  demo,
}

func main() { kit.Main(spec) }

// ticket is one event from the queue.
type ticket struct {
	ID   string
	Body string
}

// inbox is the simulated queue content. In production this is Pub/Sub,
// a bucket notification or a cron tick — outside ADK entirely.
var inbox = []ticket{
	{"T-1", "Checkout page returns 500 for every customer"},
	{"T-2", "How do I change my invoice address?"},
	{"T-3", ""}, // a malformed event: must fail loudly, not silently
	{"T-4", "Dashboard is a bit slow in the morning"},
}

func triageBrain(p kit.Prompt) kit.Reply {
	switch {
	case p.Has("500", "down", "outage", "every customer"):
		return kit.Say("P1")
	case p.Has("slow", "sometimes"):
		return kit.Say("P2")
	default:
		return kit.Say("P3")
	}
}

var errEmptyTicket = errors.New("empty ticket body")

// fileTicket is the body's side effect. It refuses an event it cannot act on.
func fileTicket(_ agent.Context, in any) (string, error) {
	p := strings.ToUpper(strings.TrimSpace(fmt.Sprint(in)))
	if len(p) < 2 || p[0] != 'P' || p[1] < '1' || p[1] > '3' {
		return "", fmt.Errorf("unexpected priority %q", p)
	}
	return "filed as " + p[:2], nil
}

// guard rejects malformed events before any model call is paid for.
func guard(_ agent.Context, in string) (string, error) {
	if strings.TrimSpace(in) == "" {
		return "", errEmptyTicket
	}
	return in, nil
}

func build(m kit.Models) (agent.Agent, error) {
	triage, err := llmagent.New(llmagent.Config{
		Name:        "triage",
		Description: "Assigns a priority to a ticket.",
		Model:       m.For("triage", triageBrain),
		Instruction: "Assign a priority to the ticket. Answer with exactly one of: P1 (outage), P2 (degraded), P3 (question).",
	})
	if err != nil {
		return nil, err
	}
	triageNode, err := workflow.NewAgentNode(triage, workflow.NodeConfig{})
	if err != nil {
		return nil, err
	}
	return workflowagent.New(workflowagent.Config{
		Name:        "ticket_body",
		Description: "Body of the ambient worker: guard → triage → file.",
		Edges: workflow.Chain(
			workflow.Start,
			workflow.NewFunctionNode("guard", guard, workflow.NodeConfig{}),
			triageNode,
			workflow.NewFunctionNode("file_ticket", fileTicket, workflow.NodeConfig{}),
		),
	})
}

// outcome is what the worker records per event.
type outcome struct {
	Ticket ticket
	Result string
	Err    error
}

// process drains the queue with a fixed number of workers. Every event gets
// a fresh session (kit.Run creates one): memory between events would need a
// memory service, not session state.
func process(ctx context.Context, m kit.Models, events <-chan ticket, workers int) ([]outcome, error) {
	body, err := build(m)
	if err != nil {
		return nil, err
	}
	var (
		mu  sync.Mutex
		out []outcome
		wg  sync.WaitGroup
	)
	for range workers {
		wg.Go(func() {
			for t := range events {
				tr, err := kit.Run(ctx, body, io.Discard, t.Body)
				mu.Lock()
				out = append(out, outcome{Ticket: t, Result: tr.Final, Err: err})
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return out, nil
}

func demo(ctx context.Context, m kit.Models, w io.Writer) error {
	events := make(chan ticket)
	go func() {
		defer close(events)
		for _, t := range inbox {
			// select picks randomly among ready cases; check cancellation
			// first so a cancelled drain stops publishing deterministically.
			if ctx.Err() != nil {
				return
			}
			select {
			case events <- t:
			case <-ctx.Done():
				return
			}
		}
	}()
	results, err := process(ctx, m, events, 2)
	if err != nil {
		return err
	}
	return report(w, results)
}

// report prints one line per event and a summary. A summary that reports a
// failure must name the reason: an ambient failure is silent until you do.
func report(w io.Writer, results []outcome) error {
	byID := map[string]outcome{}
	for _, r := range results {
		byID[r.Ticket.ID] = r
	}
	var ok, failed int
	var reasons []string
	for _, t := range inbox {
		r, seen := byID[t.ID]
		switch {
		case !seen:
			failed++
			reasons = append(reasons, t.ID+": never processed")
			fmt.Fprintf(w, "📨 %s → ❌ never processed\n", t.ID)
		case r.Err != nil:
			failed++
			reasons = append(reasons, fmt.Sprintf("%s: %v", t.ID, rootCause(r.Err)))
			fmt.Fprintf(w, "📨 %s %q → ❌ %v\n", t.ID, t.Body, rootCause(r.Err))
		default:
			ok++
			fmt.Fprintf(w, "📨 %s %q → ✅ %s\n", t.ID, t.Body, r.Result)
		}
	}
	fmt.Fprintf(w, "summary: %d events, %d ok, %d failed", len(inbox), ok, failed)
	if failed > 0 {
		fmt.Fprintf(w, " (%s)", strings.Join(reasons, "; "))
	}
	fmt.Fprintln(w)
	return nil
}

func rootCause(err error) error {
	if errors.Is(err, errEmptyTicket) {
		return errEmptyTicket
	}
	return err
}
