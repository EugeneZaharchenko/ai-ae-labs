// Eval-харнес: чи став цикл кращим — цифрами, а не відчуттям.
//
// ADK Go не має нативного eval API (станом на v2.6.0: лише заглушений REST-
// роутер; див. wiki/concepts/adk-go-eval-gap.md), тому харнес — наш: тонкий
// Go-адаптер над тим самим runner'ом, яким ходить агент. Він міряє три різні
// речі, і вони не взаємозамінні:
//
//   - accepted — цикл сказав «готово» (його власний сигнал: видимі тести +
//     панель);
//   - holdout — код справді правильний на прихованих тестах, яких ні maker, ні
//     цикл не бачили. accepted && !holdout — це і є «зелене, але хибне»;
//   - токени й час — ціна, за яку ми це отримали.
//
// Capability-eval відповідає «чи вміє агент це взагалі» (низький поріг успіху
// — норма), regression-eval — «чи не зламалось те, що вже працювало»
// (Regressions порівнює з базовим прогоном). Це різні звіти під одним словом
// «покращився».
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/adk/v2/session"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/billing"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/gate"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/usage"
)

// Options — параметри одного прогону завдання.
type Options struct {
	MaxAttempts int
	MaxTokens   int
	Gate        gate.Runner
}

// Outcome — результат одного завдання.
type Outcome struct {
	TaskID       string `json:"task"`
	Accepted     bool   `json:"accepted"`
	Attempts     int    `json:"attempts"`
	CriticRounds int    `json:"critic_rounds"`
	// HoldoutOK — код проходить приховані тести. Порожній код → false.
	HoldoutOK    bool         `json:"holdout_ok"`
	HoldoutStage string       `json:"holdout_stage,omitempty"`
	Tokens       usage.Tokens `json:"tokens"`
	ElapsedMS    int64        `json:"elapsed_ms"`
	Err          string       `json:"err,omitempty"`
}

// Run — повний слід одного прогону: Outcome плюс те, що треба для друку.
type Run struct {
	Outcome Outcome
	Code    string
	Events  []*session.Event
	Meter   *usage.Meter
}

// RunTask збирає агента під завдання, проганяє його і оцінює результат на
// прихованих тестах.
func RunTask(ctx context.Context, task billing.Task, m Models, opt Options) Run {
	meter := &usage.Meter{}
	start := time.Now()
	run := Run{Meter: meter, Outcome: Outcome{TaskID: task.ID}}

	a, err := NewAgent(Config{
		Maker:       m.Maker,
		Critics:     DefaultCritics(m.Critics[RoleCorrectness], m.Critics[RoleStyle], m.Critics[RolePerformance]),
		Grader:      ExecGrader{Runner: opt.Gate, Test: task.Visible},
		MaxAttempts: opt.MaxAttempts,
		MaxTokens:   opt.MaxTokens,
		Meter:       meter,
		Tests:       task.Visible,
	})
	if err != nil {
		run.Outcome.Err = err.Error()
		return run
	}

	res, runErr := labrun.Run(ctx, a, task.Prompt)
	run.Events = res.Events
	if runErr != nil {
		run.Outcome.Err = runErr.Error()
	} else {
		run.Code = res.Final
		run.Outcome.Accepted = res.Final != ""
	}
	run.Outcome.Attempts, run.Outcome.CriticRounds = traceCounts(res.Events)

	if run.Code != "" {
		rep := opt.Gate.Grade(ctx, gate.Spec{Code: run.Code, Test: task.Holdout})
		run.Outcome.HoldoutOK, run.Outcome.HoldoutStage = rep.OK, rep.Stage
	}
	run.Outcome.Tokens = meter.Total()
	run.Outcome.ElapsedMS = time.Since(start).Milliseconds()
	return run
}

// traceCounts читає кількість спроб і раундів критиків із StateDelta підсумку.
func traceCounts(events []*session.Event) (attempts, criticRounds int) {
	for _, ev := range events {
		if s, ok := ev.Actions.StateDelta["w6p1:summary"].(map[string]any); ok {
			return num(s["attempts"]), num(s["critic_calls"])
		}
	}
	return 0, 0
}

// num приймає і int (в пам'яті), і float64 (після JSON-раундтрипу сесії).
func num(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

// Suite — результат прогону всіх завдань; його ж пишемо як базу для regression.
type Suite struct {
	Models   map[string]string `json:"models"`
	Outcomes []Outcome         `json:"outcomes"`
}

// RunSuite проганяє завдання по черзі. Послідовно навмисно: паралельні прогони
// ділили б rate limit провайдера, і час у таблиці перестав би щось означати.
func RunSuite(ctx context.Context, tasks []billing.Task, m Models, opt Options) Suite {
	s := Suite{Models: map[string]string{}}
	for k, c := range m.Choices {
		s.Models[k] = c.Model
	}
	for _, t := range tasks {
		s.Outcomes = append(s.Outcomes, RunTask(ctx, t, m, opt).Outcome)
	}
	return s
}

// Markdown друкує таблицю для README. Ціну додає лише коли задано Price —
// тарифи змінюються щомісяця, і зашитий тариф у лабі протух би до дедлайну.
func (s Suite) Markdown(price *usage.Price) string {
	var b strings.Builder
	head := "| задача | цикл прийняв | спроб | раундів критиків | holdout | токенів (in/out) | час, с |"
	sep := "|---|---|---|---|---|---|---|"
	if price != nil {
		head += " вартість, $ |"
		sep += "---|"
	}
	b.WriteString(head + "\n" + sep + "\n")
	var total usage.Tokens
	for _, o := range s.Outcomes {
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %s | %d/%d | %.1f |",
			o.TaskID, yesNo(o.Accepted), o.Attempts, o.CriticRounds, holdoutCell(o),
			o.Tokens.Prompt, o.Tokens.Output, float64(o.ElapsedMS)/1000)
		if price != nil {
			fmt.Fprintf(&b, " %.4f |", price.Cost(o.Tokens))
		}
		b.WriteString("\n")
		total.Prompt += o.Tokens.Prompt
		total.Output += o.Tokens.Output
	}
	fmt.Fprintf(&b, "\nholdout пройдено: %d з %d; прийнято циклом, але хибне на holdout: %d; токенів усього: %d\n",
		s.holdoutPassed(), len(s.Outcomes), s.falseAccepts(), total.Total())
	return b.String()
}

func holdoutCell(o Outcome) string {
	switch {
	case o.HoldoutOK:
		return "так"
	case o.HoldoutStage != "":
		return "ні (" + o.HoldoutStage + ")"
	default:
		return "—"
	}
}

func yesNo(b bool) string {
	if b {
		return "так"
	}
	return "ні"
}

func (s Suite) holdoutPassed() (n int) {
	for _, o := range s.Outcomes {
		if o.HoldoutOK {
			n++
		}
	}
	return n
}

// falseAccepts — головна цифра eval'а: цикл сказав «готово», а код хибний.
func (s Suite) falseAccepts() (n int) {
	for _, o := range s.Outcomes {
		if o.Accepted && !o.HoldoutOK {
			n++
		}
	}
	return n
}

// Regressions повертає завдання, що проходили holdout у базовому прогоні й не
// проходять тепер. Завдання, яких не було в базі, регресією не є.
func Regressions(base, cur Suite) []string {
	was := map[string]bool{}
	for _, o := range base.Outcomes {
		was[o.TaskID] = o.HoldoutOK
	}
	var out []string
	for _, o := range cur.Outcomes {
		if was[o.TaskID] && !o.HoldoutOK {
			out = append(out, o.TaskID)
		}
	}
	return out
}

// SaveBaseline записує Suite як JSON.
func SaveBaseline(path string, s Suite) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// LoadBaseline читає Suite; відсутній файл — ErrNoBaseline, а не нульовий Suite
// (порожня база мовчки вбила б усі регресії).
func LoadBaseline(path string) (Suite, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Suite{}, ErrNoBaseline
	}
	if err != nil {
		return Suite{}, fmt.Errorf("baseline: %w", err)
	}
	var s Suite
	if err := json.Unmarshal(data, &s); err != nil {
		return Suite{}, fmt.Errorf("baseline %s: %w", path, err)
	}
	return s, nil
}

// ErrNoBaseline — бази для regression-порівняння ще немає.
var ErrNoBaseline = errors.New("базового прогону немає")
