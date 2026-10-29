// ADK-шар: тонка обгортка навколо Engine.
//
// Розподіл ролей рівно такий, як у лекції:
//   - **harness** (scaffolding) — LlmAgent, інструмент `write_strategy`, його
//     схема й перевірки: середовище, в якому агент працює;
//   - **loop** (control system) — Engine з improve.go плюс loopagent, який
//     задає, СКІЛЬКИ разів це середовище прокрутиться.
//
// Тут же матеріалізується друге, незалежне гальмо на кількість ітерацій:
// loopagent.MaxIterations — це engine-level стоп самого ADK. Він тримає навіть
// тоді, коли Budget.MaxExperiments заданий надто щедро, і навпаки. Вимкніть
// одне — друге спрацює.
//
// Верифіковано проти google.golang.org/adk/v2 v2.6.0 (станом на 10/2026).
package main

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
)

// WriteStrategyInput — контракт інструмента, яким агент пропонує нову стратегію.
//
// Path тут не декоративний: модель мусить назвати файл, у який пише. Саме це
// дає гальму №1 що перевіряти — і саме тому тест, у якому модель просить
// `../../etc/passwd`, є реальним тестом захисту, а не інсценуванням.
type WriteStrategyInput struct {
	Path    string `json:"path" jsonschema:"Path of the file to rewrite; only the single declared strategy artifact is writable"`
	Content string `json:"content" jsonschema:"Full new content of the strategy document"`
	Summary string `json:"summary" jsonschema:"One-line human-readable summary of what this change tries and why"`
}

// WriteStrategyOutput — відповідь інструмента.
type WriteStrategyOutput struct {
	Accepted bool   `json:"accepted"`
	Path     string `json:"path"`
	Note     string `json:"note"`
}

// RefusedWrite — зафіксована спроба вийти за поверхню змін.
type RefusedWrite struct {
	Path   string
	Reason string
}

// ErrNoProposal повідомляє, що модель не викликала write_strategy за цю
// ітерацію. Для циклу це підстава зупинитись, а не крутитися вхолосту.
var ErrNoProposal = errors.New("агент не запропонував зміни стратегії")

// ProposalSink — місток між ADK-шаром і Engine.
//
// Інструмент лише *кладе* пропозицію сюди; він нічого не пише на диск. Рішення
// «застосувати → виміряти → лишити чи відкотити» належить циклу, і поділ саме
// такий навмисно: інструмент, який одразу пише у файл, зробив би keep/discard
// неможливим — відкочувати було б уже нічого.
//
// Реалізує Proposer, тому Engine не знає про існування ADK.
//
// Тут же збирається вартість пропонента: AfterModel-колбек llmagent додає
// UsageMetadata кожного виклику моделі, а Propose віддає накопичене разом із
// пропозицією — чи з помилкою, бо невдала спроба теж коштує токенів.
type ProposalSink struct {
	mu      sync.Mutex
	pending []Proposal
	refused []RefusedWrite
	tokens  int
}

// NewProposalSink створює порожній sink.
func NewProposalSink() *ProposalSink { return &ProposalSink{} }

// Offer додає пропозицію в чергу.
func (s *ProposalSink) Offer(p Proposal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, p)
}

// Refuse фіксує відхилену спробу запису.
func (s *ProposalSink) Refuse(path, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refused = append(s.refused, RefusedWrite{Path: path, Reason: reason})
}

// Refusals повертає копію списку відхилених спроб.
func (s *ProposalSink) Refusals() []RefusedWrite {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RefusedWrite, len(s.refused))
	copy(out, s.refused)
	return out
}

// AddTokens додає вартість одного виклику моделі-пропонента.
func (s *ProposalSink) AddTokens(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens += n
}

// Propose реалізує Proposer: віддає найстарішу пропозицію з черги.
func (s *ProposalSink) Propose(context.Context, StrategyDoc, []Entry) (Proposal, error) {
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

// NewWriteStrategyTool будує інструмент запису стратегії, обгорнутий гальмом №1.
//
// Відмова повертається як Go-помилка навмисно: ADK віддає її моделі як
// observation, тож агент бачить «мені відмовили і чому» й може виправити
// аргументи — це той самий self-correction path, що й у тижні 4. Мовчазне
// `accepted:false` модель проігнорувала б.
func NewWriteStrategyTool(s *Surface, sink *ProposalSink) (tool.Tool, error) {
	handler := func(_ agent.Context, in WriteStrategyInput) (WriteStrategyOutput, error) {
		if _, err := s.Resolve(in.Path); err != nil {
			sink.Refuse(in.Path, err.Error())
			return WriteStrategyOutput{}, fmt.Errorf(
				"запис відхилено: %w. Єдиний файл, який можна змінювати, — %q", err, s.Rel())
		}
		if in.Content == "" {
			return WriteStrategyOutput{}, errors.New("запис відхилено: порожній вміст стратегії")
		}
		sink.Offer(Proposal{Path: in.Path, Content: in.Content, Summary: in.Summary})
		return WriteStrategyOutput{
			Accepted: true,
			Path:     in.Path,
			Note:     "пропозицію прийнято до оцінки; вона стане базовою лише якщо метрика зросте",
		}, nil
	}
	t, err := functiontool.New(functiontool.Config{
		Name: "write_strategy",
		Description: "Proposes a new version of the agent's own research strategy document. " +
			"The proposal is evaluated against a single fitness metric and kept only if it improves.",
	}, handler)
	if err != nil {
		return nil, fmt.Errorf("build write_strategy tool: %w", err)
	}
	return t, nil
}

// proposerRules — те, що ми ПРОСИМО модель робити.
//
// Варто вимовити вголос на записі: цей текст не є жодною з гарантій. Він
// підвищує шанс осмисленої пропозиції, але поверхню змін тримає Surface,
// allowlist інструментів — парсер, бюджет — Accountant, відповіді eval-набору —
// Guard, а слід — AuditLog. Prompt — це підказка; гальма — це код.
const proposerRules = `You improve a research agent's strategy document, one small change at a time.
The agent answers questions about LEDGERWORKS internal policies using the tools in "## Tools".

Rules:
- Call write_strategy exactly once, with the FULL new document (never a diff) and the exact path of the strategy file.
- Keep the structure: a "# " title, "## Metric" with exactly ONE line, "## Tools", "## Instruction" (at most 1200 characters), "## Stop conditions" with max_attempts = N. No other sections.
- Change ONE thing per experiment so the score change is attributable.
- Base the change on how the agent actually behaved (tool calls and answers below), not on guesses about the questions.
- Never write specific policy figures or answers into the instruction: it is a research method, not an answer sheet. Such a candidate is rejected.
- Do not repeat an experiment that was already discarded or refused.
- Put your hypothesis in "summary": what you changed and which behaviour you expect to improve.`

// NewProposerAgent будує LlmAgent, який пропонує зміни стратегії.
//
// Інструкція збирається InstructionProvider-ом на КОЖЕН запуск: чинна
// стратегія, її поведінка й історія змінюються між ітераціями, а Instruction
// як рядок ADK ще й трактує як шаблон із {плейсхолдерами} — тоді як текст
// стратегії може містити довільні фігурні дужки.
func NewProposerAgent(m model.LLM, s *Surface, sink *ProposalSink, brief func() string) (agent.Agent, error) {
	writeTool, err := NewWriteStrategyTool(s, sink)
	if err != nil {
		return nil, err
	}
	temp := float32(0.4)
	a, err := llmagent.New(llmagent.Config{
		Name:        "strategy_proposer",
		Model:       m,
		Description: "Proposes one bounded change to the agent's own strategy document.",
		InstructionProvider: func(agent.ReadonlyContext) (string, error) {
			out := proposerRules + "\n\nStrategy file path: " + s.Rel() + "\n\n"
			if brief != nil {
				out += brief()
			}
			return out, nil
		},
		GenerateContentConfig: &genai.GenerateContentConfig{Temperature: &temp},
		Tools:                 []tool.Tool{writeTool},
		AfterModelCallbacks: []llmagent.AfterModelCallback{
			func(_ agent.Context, resp *model.LLMResponse, _ error) (*model.LLMResponse, error) {
				sink.AddTokens(usageTokens(resp))
				return nil, nil
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build proposer agent: %w", err)
	}
	return a, nil
}

// usageTokens — токени одного виклику моделі за UsageMetadata.
func usageTokens(resp *model.LLMResponse) int {
	if resp == nil || resp.UsageMetadata == nil {
		return 0
	}
	u := resp.UsageMetadata
	if u.TotalTokenCount > 0 {
		return int(u.TotalTokenCount)
	}
	return int(u.PromptTokenCount + u.CandidatesTokenCount + u.ThoughtsTokenCount)
}

// newEvaluatorAgent — під-агент, що виконує рівно один крок Engine.
//
// Він же несе умову дострокового виходу: коли бюджет вичерпано, подія
// позначається Actions.Escalate, і loopagent завершує цикл, не докручуючи
// решту ітерацій.
func newEvaluatorAgent(e *Engine) (agent.Agent, error) {
	run := func(ictx agent.InvocationContext) iter.Seq2[*session.Event, error] {
		return func(yield func(*session.Event, error) bool) {
			if err := e.Start(ictx); err != nil {
				yield(nil, err)
				return
			}
			entry, reason, err := e.Step(ictx)
			if err != nil {
				yield(nil, err)
				return
			}
			if reason != "" {
				yield(textEvent(fmt.Sprintf("цикл зупинено: %s", reason), true), nil)
				return
			}
			yield(textEvent(fmt.Sprintf("[exp %d] %s — %s", entry.ID, entry.Verdict, entry.Reason), false), nil)
			if why := e.Exhausted(); why != "" {
				// Бюджет вичерпано саме цим експериментом: закриваємо цикл
				// ескалацією одразу, а не платимо пропоненту за виклик, який
				// ніхто не оцінить.
				yield(textEvent("цикл зупинено: "+string(why), true), nil)
			}
		}
	}
	a, err := agent.New(agent.Config{
		Name:        "experiment_evaluator",
		Description: "Applies one proposal, scores it, and keeps or reverts it.",
		Run:         run,
	})
	if err != nil {
		return nil, fmt.Errorf("build evaluator agent: %w", err)
	}
	return a, nil
}

// textEvent — подія з текстом і (за потреби) сигналом ескалації.
func textEvent(text string, escalate bool) *session.Event {
	return &session.Event{
		LLMResponse: model.LLMResponse{
			Content: genai.NewContentFromText(text, genai.RoleModel),
		},
		Actions: session.EventActions{Escalate: escalate},
	}
}

// NewSelfImproveAgent збирає цикл самополіпшення як loopagent.
//
// maxIterations передається окремо від Budget навмисно — це ДРУГЕ, незалежне
// гальмо на ту саму вісь. І воно ж пояснює, чому параметр обовʼязковий:
// у loopagent MaxIterations == 0 означає «крутитися вічно або доки хтось не
// ескалює». Нуль тут — не «за замовчуванням», а нічний прогін без нагляду,
// тому ми відхиляємо його явно.
func NewSelfImproveAgent(e *Engine, proposer agent.Agent, maxIterations int) (agent.Agent, error) {
	if e == nil {
		return nil, errors.New("self-improve: не задано Engine")
	}
	if proposer == nil {
		return nil, errors.New("self-improve: не задано агента-пропонента")
	}
	if maxIterations < 1 {
		return nil, errors.New(
			"self-improve: maxIterations має бути ≥ 1 — у loopagent 0 означає «нескінченно»")
	}
	evaluator, err := newEvaluatorAgent(e)
	if err != nil {
		return nil, err
	}
	a, err := loopagent.New(loopagent.Config{
		MaxIterations: uint(maxIterations),
		AgentConfig: agent.Config{
			Name:        "self_improve_loop",
			Description: "Bounded self-improvement loop: propose, evaluate, keep or revert.",
			SubAgents:   []agent.Agent{proposer, evaluator},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build self-improve loop: %w", err)
	}
	return a, nil
}
