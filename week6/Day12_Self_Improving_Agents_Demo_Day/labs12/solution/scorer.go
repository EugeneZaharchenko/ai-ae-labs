// Вимірювання: воркер-агент, який читає стратегію, і LLMScorer, який проганяє
// його по eval-набору та перетворює результат на ОДНЕ число.
//
// Це та частина, що в початковій версії ДЗ була фікстурою. Тепер вимір — це
// справжні виклики моделі: кожен кейс — окремий запуск llmagent з реальними
// function-calling інструментами kb_search/kb_read, а вартість береться з
// UsageMetadata. Метрика при цьому лишається чистою функцією від тексту
// відповіді (ledgerkb.Score): модель відповідає, але ЛИШЕ детермінований код
// вирішує, скільки це варте. Саме тому її важко «домовитись» — судді нема.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/ledgerkb"
)

// harnessPreamble — частина системної інструкції, яку агент НЕ редагує. Вона
// живе в коді, а не в strategy.md: домен, заборона вигадувати й мова відповіді
// належать людині. Стратегія додає до неї лише метод роботи.
const harnessPreamble = `You are the LEDGERWORKS policy research assistant.
Answer only from the policy documents reachable through your tools; never invent figures.
Answer in the language of the question.

Research method (set by the strategy file):
`

// DefaultMaxModelCalls — стеля викликів моделі на одне запитання. Без неї
// воркер, що зациклився на порожньому пошуку, коштує необмежено (див. ніч із
// 47 ітераціями у Webinar 7).
const DefaultMaxModelCalls = 8

// NewWorkerAgent будує дослідницького агента за стратегією.
//
// Стратегія впливає на агента двома каналами, і обидва справжні: Instruction
// стає системною інструкцією, а Tools — буквально списком інструментів, які
// отримує модель.
func NewWorkerAgent(m model.LLM, kb *ledgerkb.KB, doc StrategyDoc, maxCalls int) (agent.Agent, error) {
	tools, err := kb.Tools(doc.Tools)
	if err != nil {
		return nil, err
	}
	if maxCalls < 1 {
		maxCalls = DefaultMaxModelCalls
	}
	temp := float32(0)
	var calls int
	capCalls := func(agent.Context, *model.LLMRequest) (*model.LLMResponse, error) {
		calls++
		if calls > maxCalls {
			return &model.LLMResponse{Content: genai.NewContentFromText(
				"Stopped: the model-call cap for one question was reached.", genai.RoleModel)}, nil
		}
		return nil, nil
	}
	a, err := llmagent.New(llmagent.Config{
		Name:        "ledger_research_agent",
		Description: "Answers LEDGERWORKS policy questions from the knowledge base.",
		Model:       m,
		InstructionProvider: func(agent.ReadonlyContext) (string, error) {
			return harnessPreamble + doc.Instruction, nil
		},
		Tools:                 tools,
		GenerateContentConfig: &genai.GenerateContentConfig{Temperature: &temp},
		BeforeModelCallbacks:  []llmagent.BeforeModelCallback{capCalls},
	})
	if err != nil {
		return nil, fmt.Errorf("build worker agent: %w", err)
	}
	return a, nil
}

// CaseResult — підсумок одного eval-запитання.
type CaseResult struct {
	ID      string
	Answer  string
	Calls   []string
	Found   int
	Total   int
	Tokens  int
	Failure error
}

// LLMScorer — Scorer, що міряє стратегію реальною моделлю на eval-наборі.
type LLMScorer struct {
	Model    model.LLM
	KB       *ledgerkb.KB
	Cases    []ledgerkb.Case
	Parallel int
	MaxCalls int
}

// RunCases проганяє всі кейси (не більше Parallel одночасно) і повертає
// результати в порядку кейсів. Помилка одного кейсу не губить решту: її видно
// в CaseResult.Failure.
func (s *LLMScorer) RunCases(ctx context.Context, doc StrategyDoc) []CaseResult {
	out := make([]CaseResult, len(s.Cases))
	par := s.Parallel
	if par < 1 {
		par = 1
	}
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	for i, c := range s.Cases {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = s.runCase(ctx, doc, c)
		}()
	}
	wg.Wait()
	return out
}

func (s *LLMScorer) runCase(ctx context.Context, doc StrategyDoc, c ledgerkb.Case) CaseResult {
	r := CaseResult{ID: c.ID, Total: len(c.Facts)}
	a, err := NewWorkerAgent(s.Model, s.KB, doc, s.MaxCalls)
	if err != nil {
		r.Failure = err
		return r
	}
	res, err := labrun.Run(ctx, a, c.Question)
	for _, e := range res.Events {
		r.Tokens += usageTokens(&e.LLMResponse)
	}
	r.Calls = res.ToolCalls
	r.Answer = res.Final
	if err != nil {
		r.Failure = err
		return r
	}
	r.Found, _ = ledgerkb.Score(res.Final, c.Facts)
	return r
}

// Score реалізує Scorer.
//
// Помилка будь-якого кейсу — помилка виміру, а не нуль за кейс: інакше
// мережевий збій тихо виглядав би як «стратегія стала гіршою» і міг би
// спричинити хибний discard (або, на базовому вимірі, хибну базу).
func (s *LLMScorer) Score(ctx context.Context, doc StrategyDoc) (Measurement, error) {
	results := s.RunCases(ctx, doc)
	m := Measurement{}
	found, total := 0, 0
	var errs []error
	for _, r := range results {
		m.Tokens += r.Tokens
		found += r.Found
		total += r.Total
		if r.Failure != nil {
			errs = append(errs, fmt.Errorf("кейс %s: %w", r.ID, r.Failure))
		}
	}
	m.Detail = Describe(results)
	if err := errors.Join(errs...); err != nil {
		return m, err
	}
	if total > 0 {
		m.Score = float64(found) / float64(total)
	}
	return m, nil
}

// Describe — розбір поведінки для пропонента й для аудиту. Містить питання не
// цілком, а лише ID, виклики інструментів і початок ВЛАСНОЇ відповіді агента —
// ніколи золоті факти.
func Describe(results []CaseResult) string {
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "- %s: %d/%d facts; tool calls: [%s]; answer: %q\n",
			r.ID, r.Found, r.Total, strings.Join(r.Calls, ", "), clip(r.Answer, 140))
	}
	return strings.TrimRight(b.String(), "\n")
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// NoGoldenLeak — Guard проти найпростішого reward hacking: вписати відповіді
// eval-набору в саму стратегію. Перевіряє і Instruction, і весь сирий текст.
func NoGoldenLeak(cases []ledgerkb.Case) Guard {
	return func(doc StrategyDoc) error {
		if leaks := ledgerkb.Leaks(doc.Raw, cases); len(leaks) > 0 {
			return fmt.Errorf("стратегія містить відповіді з eval-набору (%s): це шпаргалка, а не метод",
				strings.Join(leaks, "; "))
		}
		return nil
	}
}
