package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"
)

// The collaborative workflow engine.
//
//	Start → plan ─┬→ risk_desk ────────┐
//	              ├→ compliance_desk ──┼→ gather (JoinNode) → synthesize
//	              ├→ integration_desk ─┤      → approval_gate ⏸ → dispatch
//	              └────────────────────┘
//
// Delegation is done by the graph, not by a model choosing sub-agents: the
// coordinator's job — keep the task whole, split it, merge it — is structure,
// and structure belongs in edges a reviewer can read. plan has an edge to
// gather as well as to the desks, so the join also carries the plan.

// Engine holds what the nodes need. No package globals: two runs in one test
// binary must not share a desk counter or an outbox.
type Engine struct {
	LLM      model.LLM
	FailDesk string // inject a failure into one desk, to watch containment

	mu      sync.Mutex
	calls   map[string]int // model calls per desk — the resume proof
	outbox  []Brief        // what dispatch actually sent
	pauses  int            // how many times the gate asked
	resumes int            // how many times it got an answer
}

// Build assembles the graph.
func (e *Engine) Build() (agent.Agent, error) {
	plan := workflow.NewFunctionNode("plan", func(_ agent.Context, task string) (Plan, error) {
		if strings.TrimSpace(task) == "" {
			return Plan{}, errors.New("plan: empty task")
		}
		return Plan{Task: task, Desks: desks}, nil
	}, workflow.NodeConfig{})

	deskNodes := make([]workflow.Node, 0, len(desks))
	for _, d := range desks {
		deskNodes = append(deskNodes, workflow.NewFunctionNode(d, e.desk(d), workflow.NodeConfig{}))
	}

	gather := workflow.NewJoinNode("gather")
	synthesize := workflow.NewFunctionNode("synthesize", func(_ agent.Context, joined map[string]any) (Brief, error) {
		p, ok := joined["plan"].(Plan)
		if !ok {
			return Brief{}, fmt.Errorf("synthesize: the join carried no plan (keys: %d)", len(joined))
		}
		return Merge(p, joined), nil
	}, workflow.NodeConfig{})

	rerun := true
	gate := workflow.NewEmittingFunctionNode("approval_gate", e.gate, workflow.NodeConfig{RerunOnResume: &rerun})
	dispatch := workflow.NewFunctionNode("dispatch", e.dispatch, workflow.NodeConfig{})

	eb := workflow.NewEdgeBuilder()
	eb.Add(workflow.Start, plan)
	eb.AddFanOut(plan, deskNodes...)
	eb.AddFanIn(gather, append([]workflow.Node{plan}, deskNodes...)...)
	eb.Add(gather, synthesize)
	eb.Add(synthesize, gate)
	eb.Add(gate, dispatch)

	return workflowagent.New(workflowagent.Config{
		Name:        "collaborative_workflow_engine",
		Description: "Coordinator and desks: Plan-Execute with a human approval before anything leaves the building.",
		Edges:       eb.Build(),
	})
}

// desk is one specialist: one grounded model call. A failure is contained into
// a Section with a Gap — the run goes on, and the brief says what is missing.
func (e *Engine) desk(name string) func(agent.Context, Plan) (Section, error) {
	return func(ctx agent.Context, p Plan) (Section, error) {
		e.mu.Lock()
		e.calls[name]++
		e.mu.Unlock()

		if name == e.FailDesk {
			return Section{Desk: name, Gap: "the desk's data source returned 503 (injected with -fail-desk)"}, nil
		}
		text, err := e.ask(ctx, name, p.Task)
		if err != nil {
			return Section{Desk: name, Gap: err.Error()}, nil
		}
		if strings.TrimSpace(text) == "" {
			return Section{Desk: name, Gap: "the model returned no text"}, nil
		}
		return Section{Desk: name, Text: text}, nil
	}
}

// ask makes the desk's model call. Each desk sees its own instruction and its
// own fact sheet and nothing from its neighbours: the isolation is that no
// other desk's text is in the request.
func (e *Engine) ask(ctx agent.Context, name, task string) (string, error) {
	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText(task, genai.RoleUser)},
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText(deskInstruction(name), genai.RoleUser),
		},
	}
	var b strings.Builder
	for resp, err := range e.LLM.GenerateContent(ctx, req, false) {
		if err != nil {
			return "", fmt.Errorf("model: %w", err)
		}
		if resp == nil || resp.Partial || resp.Content == nil {
			continue
		}
		for _, p := range resp.Content.Parts {
			if !p.Thought {
				b.WriteString(p.Text)
			}
		}
	}
	return b.String(), nil
}

func deskInstruction(name string) string {
	return fmt.Sprintf(`You are the %s of LEDGERWORKS, writing one section of a board brief.
Use ONLY the facts below. If a question cannot be answered from them, say so.
Write three short bullet points, no preamble.

Facts:
%s`, name, facts[name])
}

// gate is the human-in-the-loop pause before the one irreversible step.
//
// First pass: ResumeOrRequestInput emits a request and returns
// ErrNodeInterrupted; the graph pauses and its state is in the session. When
// the answer arrives, RerunOnResume re-runs THIS node with the same input —
// not the graph. The desks are not asked again, which is why their counters
// are printed: a resume that re-ran three model calls would bill them twice.
//
// The InterruptID carries the document hash, so the question is about one
// exact brief, and the invocation ID, so the same brief in a later run does not
// inherit an earlier answer.
func (e *Engine) gate(ctx agent.Context, b Brief, emit func(*session.Event) error) (Outcome, error) {
	reply, err := workflow.ResumeOrRequestInput(ctx, emit, session.RequestInput{
		InterruptID: "board-approval-" + b.Hash + "-" + ctx.InvocationID(),
		Message:     "Send this brief to the board?\n\n" + b.Text,
		Payload:     map[string]any{"hash": b.Hash, "sections": b.Count, "gaps": b.Gaps},
	})
	if err != nil {
		if errors.Is(err, workflow.ErrNodeInterrupted) {
			e.mu.Lock()
			e.pauses++
			e.mu.Unlock()
		}
		return Outcome{}, err
	}
	e.mu.Lock()
	e.resumes++
	e.mu.Unlock()
	d, err := DecodeDecision(reply)
	if err != nil {
		return Outcome{}, fmt.Errorf("approval_gate: %w", err)
	}
	return Outcome{Brief: b, Decision: d}, nil
}

// dispatch is the only node that acts outside the process, and it sits
// strictly after the gate. It re-checks the decision rather than trusting the
// edge: a rewired graph must not be able to send an unapproved brief.
func (e *Engine) dispatch(_ agent.Context, o Outcome) (string, error) {
	if err := o.Sendable(); err != nil {
		return "not sent: " + err.Error(), nil
	}
	e.mu.Lock()
	e.outbox = append(e.outbox, o.Brief)
	e.mu.Unlock()
	return fmt.Sprintf("sent brief %s (%d sections) to the board; approved by %s", o.Brief.Hash, o.Brief.Count, o.Decision.By), nil
}

// Stats is a snapshot for the report.
func (e *Engine) Stats() (calls map[string]int, sent []Brief, pauses, resumes int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	calls = map[string]int{}
	for k, v := range e.calls {
		calls[k] = v
	}
	return calls, append([]Brief(nil), e.outbox...), e.pauses, e.resumes
}
