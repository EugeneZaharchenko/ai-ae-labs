package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/adkrun"
)

// The ADK side: a worker agent that answers eval questions under a strategy, and
// a proposer agent that writes the next strategy.
//
// These are two different roles on purpose. The proposer sees how the worker
// behaved (its answers and per-question scores) but never the golden facts: a
// proposer that can read the answer key does not research a better method, it
// transcribes the key into the instruction, and the metric rises while the
// agent learns nothing.

// harnessPreamble is the part of the worker's instruction the strategy cannot
// change. It lives in code: the domain, the ban on inventing figures and the
// answer language belong to a human. The strategy adds a method on top.
const harnessPreamble = `You are the LEDGERWORKS policy research assistant.
Answer only from the policy documents reachable through your tools; never invent figures.
Answer in the language of the question.

Research method (set by the strategy file):
`

// maxModelCalls is the per-question budget. Without it, a worker stuck on an
// empty search costs without bound — the 47-iteration night from week 4.
const maxModelCalls = 6

// ErrCallBudget marks a worker that used its model-call budget on one question.
var ErrCallBudget = errors.New("worker exceeded its model-call budget for one question")

const callsKey = "temp:worker_model_calls"

// Engine runs the agents. LLM is the worker's model, Proposer the proposer's;
// live they are the same model, offline they are two different stand-ins.
type Engine struct {
	Worker   model.LLM
	Proposer model.LLM
}

// NewWorker builds the worker for a strategy and a KB. The strategy reaches the
// agent through two channels and both are real: its instruction becomes the
// system instruction, and its tool list is literally the tools the model gets.
func (e *Engine) NewWorker(s Strategy, kb *KB) (agent.Agent, error) {
	tools, err := kb.Tools(s.Tools)
	if err != nil {
		return nil, err
	}
	return llmagent.New(llmagent.Config{
		Name:                  "policy_researcher",
		Description:           "Answers LEDGERWORKS policy questions from the knowledge base.",
		Model:                 e.Worker,
		Instruction:           harnessPreamble + s.Instruction,
		Tools:                 tools,
		GenerateContentConfig: &genai.GenerateContentConfig{Temperature: genai.Ptr[float32](0)},
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{
			func(ctx agent.Context, _ *model.LLMRequest) (*model.LLMResponse, error) {
				n := 0
				if v, err := ctx.State().Get(callsKey); err == nil {
					n, _ = v.(int)
				}
				if n >= maxModelCalls {
					return nil, fmt.Errorf("%d model calls: %w", n, ErrCallBudget)
				}
				return nil, ctx.State().Set(callsKey, n+1)
			},
		},
	})
}

// Measure runs every dev question under the strategy and scores the answers.
// A worker that blows its call budget on one question scores zero on it — that
// is a real property of the strategy, not an infrastructure failure — while any
// other error aborts the run.
func (e *Engine) Measure(ctx context.Context, s Strategy) (Measurement, error) {
	m := Measurement{Answers: map[string]string{}, PerCase: map[string]float64{}}
	kb := NewKB()
	for _, c := range Dev() {
		w, err := e.NewWorker(s, kb)
		if err != nil {
			return m, err
		}
		// adkrun.Run builds a fresh in-memory session per call: each question
		// starts with no memory of the previous one, which is what an eval needs.
		answer, err := adkrun.Run(ctx, w, c.Question, nil)
		if err != nil && !errors.Is(err, ErrCallBudget) {
			return m, fmt.Errorf("question %s: %w", c.ID, err)
		}
		if errors.Is(err, ErrCallBudget) {
			answer = ""
		}
		found, _ := c.Found(answer)
		m.Found += found
		m.Total += len(c.Facts)
		m.Answers[c.ID] = answer
		m.PerCase[c.ID] = float64(found) / float64(len(c.Facts))
	}
	m.Reads = kb.Reads()
	return m, nil
}

const proposerInstruction = `ROLE: proposer
You improve a research strategy for a policy-question agent. You will see the
current strategy file, the tools that exist, and how the agent did on each
question (its answer and a score). You will NOT see the correct answers.

Reply with ONE code block containing the complete new strategy file in exactly
this form, and nothing else:

# <short title describing the change>
tools: <comma-separated tool names>

<instruction for the agent: how to research a question>

Change one thing at a time. The instruction is at most 600 characters.`

// Propose asks the proposer for the next strategy and returns its raw answer;
// the loop parses and checks it.
func (e *Engine) Propose(ctx context.Context, best Strategy, last Measurement) (string, error) {
	p, err := llmagent.New(llmagent.Config{
		Name:                  "strategy_proposer",
		Description:           "Proposes the next research strategy.",
		Model:                 e.Proposer,
		Instruction:           proposerInstruction,
		GenerateContentConfig: &genai.GenerateContentConfig{Temperature: genai.Ptr[float32](0.4)},
	})
	if err != nil {
		return "", err
	}
	return adkrun.Run(ctx, p, proposerPrompt(best, last), nil)
}

func proposerPrompt(best Strategy, last Measurement) string {
	var b strings.Builder
	b.WriteString("Current strategy file:\n```\n" + best.Render() + "```\n\n")
	fmt.Fprintf(&b, "Tools that exist: %s\n", strings.Join(allowedTools, ", "))
	fmt.Fprintf(&b, "kb_search returns only a one-sentence scope for each document; kb_read returns the full text.\n\n")
	fmt.Fprintf(&b, "How it did (score %.2f):\n", last.Score())
	for _, c := range Dev() {
		a := strings.Join(strings.Fields(last.Answers[c.ID]), " ")
		if len(a) > 220 {
			a = a[:220] + "…"
		}
		fmt.Fprintf(&b, "- %s score %.2f — answer: %q\n", c.ID, last.PerCase[c.ID], a)
	}
	return b.String()
}
