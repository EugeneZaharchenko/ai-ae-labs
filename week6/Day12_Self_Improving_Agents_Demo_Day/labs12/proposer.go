package main

// ADK layer of the self-improvement loop (adapted from solution/agent.go):
//   - harness: the proposer LlmAgent and its write_strategy tool;
//   - loop: Engine (improve.go) driven by a loopagent whose MaxIterations is a
//     second, independent cap on iterations. Disable one — the other holds.

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagents/loopagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/sandbox"
)

// WriteStrategyInput is the tool contract. The model must name the file it
// writes: that is what gives brake (a) something to check.
type WriteStrategyInput struct {
	Path    string `json:"path" jsonschema:"Path of the file to rewrite; only strategy.md is writable"`
	Content string `json:"content" jsonschema:"Full new content of the strategy document"`
	Summary string `json:"summary" jsonschema:"One line: what this change tries and which behaviour should improve"`
}

// WriteStrategyOutput is the tool answer.
type WriteStrategyOutput struct {
	Accepted bool   `json:"accepted"`
	Note     string `json:"note"`
}

// ErrNoProposal: the model did not call write_strategy this iteration.
var ErrNoProposal = errors.New("агент не запропонував зміни стратегії")

// ProposalSink bridges ADK and Engine. The tool only queues a proposal; it
// writes nothing to disk — apply/score/keep-or-revert belongs to the loop.
type ProposalSink struct {
	root    string
	mu      sync.Mutex
	pending []Proposal
	tokens  int
}

// NewProposalSink creates an empty sink for the strategy in root.
func NewProposalSink(root string) *ProposalSink { return &ProposalSink{root: root} }

// AddTokens books one proposer model call.
func (s *ProposalSink) AddTokens(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens += n
}

// Offer queues a proposal.
func (s *ProposalSink) Offer(p Proposal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, p)
}

// Propose implements Proposer: the oldest queued proposal plus its cost.
func (s *ProposalSink) Propose(context.Context, Strategy, []Entry) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokens := s.tokens
	s.tokens = 0
	if len(s.pending) == 0 {
		return Proposal{Tokens: tokens}, ErrNoProposal
	}
	p := s.pending[0]
	s.pending = s.pending[1:]
	p.Tokens = tokens
	return p, nil
}

// NewWriteStrategyTool builds write_strategy, wrapped in brake (a). A refusal
// is a Go error on purpose: the model sees it as an observation and can fix
// its arguments.
func NewWriteStrategyTool(sink *ProposalSink) (tool.Tool, error) {
	handler := func(_ agent.Context, in WriteStrategyInput) (WriteStrategyOutput, error) {
		if !sandbox.IsMutable(sink.root, in.Path) || in.Path != sandbox.StrategyFile {
			return WriteStrategyOutput{}, fmt.Errorf("запис відхилено: %q поза поверхнею змін; єдиний файл стратегії — %q",
				in.Path, sandbox.StrategyFile)
		}
		if in.Content == "" {
			return WriteStrategyOutput{}, errors.New("запис відхилено: порожній вміст")
		}
		sink.Offer(Proposal{Path: in.Path, Content: in.Content, Summary: in.Summary})
		return WriteStrategyOutput{Accepted: true,
			Note: "прийнято до оцінки; стане чинною лише якщо метрика зросте"}, nil
	}
	t, err := functiontool.New(functiontool.Config{
		Name:        "write_strategy",
		Description: "Proposes a new version of the research agent's strategy.md. It is scored on one metric and kept only if it improves.",
	}, handler)
	if err != nil {
		return nil, fmt.Errorf("write_strategy: %w", err)
	}
	return t, nil
}

// proposerRules are a request, not a guarantee: the surface is held by the
// sandbox, the format by the parser, the budget by the accountant, the eval
// answers by a guard and the trail by the audit log. Prompt is a hint; brakes are code.
const proposerRules = `You improve the research strategy of a LEDGERWORKS policy research agent, one small change at a time.
The agent: plans sub-queries, searches the knowledge base (kb_search: top_k passages per sub-query, then the
full text of read_docs best documents) and optionally an entity graph (graph_lookup), drafts a report with
[DOC-ID] citations following "## Instruction", then runs the critics in "## Critics" order (stops at the first
failure, revises up to critic_rounds times). Each eval question has 60 seconds; slow strategies lose answers.

Rules:
- Call write_strategy exactly once with path "strategy.md" and the FULL new document (never a diff).
- Keep the format: "# " title; sections Metric (exactly one line), Sources, Search (max_subqueries 1-6,
  top_k 1-8, read_docs 0-4), Critics (from: citations, completeness, clarity), Instruction (≤1200 chars),
  Stop conditions (max_attempts 1-50, critic_rounds 1-3). No other sections.
- Change ONE thing per experiment, so the score change can be attributed.
- Base it on how the agent actually behaved (below), not on guesses about the questions.
- Never write policy figures, names or answers into the strategy: it is a method, not an answer sheet.
- Do not repeat an experiment that was already discarded or refused.
- Put your hypothesis in "summary".`

// NewProposerAgent builds the LlmAgent that proposes strategy changes. The
// instruction is rebuilt every iteration from Engine.Brief.
func NewProposerAgent(m model.LLM, sink *ProposalSink, brief func() string) (agent.Agent, error) {
	t, err := NewWriteStrategyTool(sink)
	if err != nil {
		return nil, err
	}
	temp := float32(0.4)
	a, err := llmagent.New(llmagent.Config{
		Name:        "strategy_proposer",
		Model:       m,
		Description: "Proposes one bounded change to strategy.md.",
		InstructionProvider: func(agent.ReadonlyContext) (string, error) {
			return proposerRules + "\n\n" + brief(), nil
		},
		GenerateContentConfig: &genai.GenerateContentConfig{Temperature: &temp},
		Tools:                 []tool.Tool{t},
		AfterModelCallbacks: []llmagent.AfterModelCallback{
			func(_ agent.Context, resp *model.LLMResponse, _ error) (*model.LLMResponse, error) {
				sink.AddTokens(usageTokens(resp))
				return nil, nil
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("proposer: %w", err)
	}
	return a, nil
}

// newEvaluatorAgent runs exactly one Engine step per loop iteration and
// escalates (ends the loop) when the budget is exhausted.
func newEvaluatorAgent(e *Engine, onEntry func(Entry)) (agent.Agent, error) {
	run := func(ictx agent.InvocationContext) iter.Seq2[*session.Event, error] {
		return func(yield func(*session.Event, error) bool) {
			if err := e.Start(ictx); err != nil {
				yield(nil, err)
				return
			}
			entry, why, err := e.Step(ictx)
			if err != nil {
				yield(nil, err)
				return
			}
			if why != "" {
				yield(textEvent("цикл зупинено: "+string(why), true), nil)
				return
			}
			if onEntry != nil {
				onEntry(entry)
			}
			if !yield(textEvent(fmt.Sprintf("[exp %d] %s — %s", entry.ID, entry.Verdict, entry.Reason), false), nil) {
				return
			}
			if why := e.Exhausted(); why != "" {
				yield(textEvent("цикл зупинено: "+string(why), true), nil)
			}
		}
	}
	return agent.New(agent.Config{Name: "experiment_evaluator", Description: "Applies, scores, keeps or reverts one proposal.", Run: run})
}

func textEvent(text string, escalate bool) *session.Event {
	return &session.Event{
		LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(text, genai.RoleModel)},
		Actions:     session.EventActions{Escalate: escalate},
	}
}

// NewSelfImproveAgent is the loop as a loopagent. maxIterations is required:
// in loopagent 0 means "forever", so we reject it.
func NewSelfImproveAgent(e *Engine, proposer agent.Agent, maxIterations int, onEntry func(Entry)) (agent.Agent, error) {
	if e == nil || proposer == nil {
		return nil, errors.New("self-improve: потрібні Engine і пропонент")
	}
	if maxIterations < 1 {
		return nil, errors.New("self-improve: maxIterations має бути ≥ 1 — у loopagent 0 означає «нескінченно»")
	}
	ev, err := newEvaluatorAgent(e, onEntry)
	if err != nil {
		return nil, err
	}
	return loopagent.New(loopagent.Config{
		MaxIterations: uint(maxIterations),
		AgentConfig: agent.Config{
			Name:        "self_improve_loop",
			Description: "Bounded self-improvement: propose, evaluate, keep or revert.",
			SubAgents:   []agent.Agent{proposer, ev},
		},
	})
}
