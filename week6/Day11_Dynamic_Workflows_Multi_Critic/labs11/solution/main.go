// Референс ДЗ 11: динамічний workflow із панеллю критиків, справжнім
// грейдером (go build / vet / test) і живою моделлю.
//
//	go run ./week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/solution              # одне завдання
//	go run ./week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/solution -eval         # усі завдання + таблиця
//
// Модель беремо з apps/.env через internal/modelcfg; роль можна перевизначити
// (LAB11_MAKER_MODEL, LAB11_CORRECTNESS_MODEL, LAB11_STYLE_MODEL,
// LAB11_PERFORMANCE_MODEL). Якщо провайдера немає — офлайн-режим зі
// скриптованими моделями, і програма ПИШЕ, чому саме.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/billing"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/gate"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/usage"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, DefaultResolver()); err != nil {
		fmt.Fprintln(os.Stderr, "помилка:", err)
		os.Exit(1)
	}
}

// config — розібрані прапорці.
type config struct {
	task        string
	eval        bool
	offline     bool
	maxAttempts int
	maxTokens   int
	baseline    string
	writeBase   bool
	priceIn     float64
	priceOut    float64
}

func parseFlags(args []string, errOut io.Writer) (config, error) {
	var c config
	fs := flag.NewFlagSet("solution", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.StringVar(&c.task, "task", "prorate_refund", "id завдання: "+taskIDs())
	fs.BoolVar(&c.eval, "eval", false, "проґнати всі завдання й надрукувати eval-таблицю")
	fs.BoolVar(&c.offline, "offline", false, "примусово скриптовані моделі (gate і граф справжні)")
	fs.IntVar(&c.maxAttempts, "max-attempts", DefaultMaxAttempts, "бюджет спроб")
	fs.IntVar(&c.maxTokens, "max-tokens", 0, "токенний бюджет циклу (0 — без ліміту)")
	fs.StringVar(&c.baseline, "baseline", "", "файл бази для regression-порівняння (JSON)")
	fs.BoolVar(&c.writeBase, "write-baseline", false, "записати результат у -baseline")
	fs.Float64Var(&c.priceIn, "price-in", 0, "тариф, $ за 1M вхідних токенів (0 — не показувати вартість)")
	fs.Float64Var(&c.priceOut, "price-out", 0, "тариф, $ за 1M вихідних токенів")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if c.writeBase && c.baseline == "" {
		return config{}, errors.New("-write-baseline потребує -baseline <файл>")
	}
	return c, nil
}

func taskIDs() string {
	var ids []string
	for _, t := range billing.Tasks() {
		ids = append(ids, t.ID)
	}
	return strings.Join(ids, ", ")
}

// run — уся логіка команди; main лише її викликає (AGENTS.md §4: тонкий main).
func run(ctx context.Context, args []string, out io.Writer, res Resolver) error {
	cfg, err := parseFlags(args, out)
	if err != nil {
		return err
	}
	tasks := billing.Tasks()
	if !cfg.eval {
		t, ok := billing.ByID(cfg.task)
		if !ok {
			return fmt.Errorf("невідоме завдання %q; доступні: %s", cfg.task, taskIDs())
		}
		tasks = []billing.Task{t}
	}

	// Помилка вибору провайдера — не привід падати: це офлайн-режим, але з
	// названою причиною (порожній «0 результатів» без причини гірший за помилку).
	var models Models
	var merr error
	if cfg.offline {
		merr = errors.New("запитано прапорцем -offline")
	} else {
		_ = modelcfg.LoadEnv(".")
		models, merr = res.Resolve(ctx)
	}
	offline := merr != nil
	if offline {
		fmt.Fprintf(out, "ОФЛАЙН-РЕЖИМ: %v\n"+
			"Моделі скриптовані, gate і граф — справжні. Ключ або Ollama: див. Homework.md, розділ «Якщо щось не працює».\n\n",
			firstLine(merr))
	} else {
		fmt.Fprint(out, "Моделі:\n", models.Describe(), "\n")
	}

	opt := Options{MaxAttempts: cfg.maxAttempts, MaxTokens: cfg.maxTokens, Gate: gate.Runner{}}
	var price *usage.Price
	if cfg.priceIn > 0 || cfg.priceOut > 0 {
		price = &usage.Price{InPerM: cfg.priceIn, OutPerM: cfg.priceOut}
	}

	if !cfg.eval {
		m := models
		if offline {
			m = OfflineModels(tasks[0])
		}
		r := RunTask(ctx, tasks[0], m, opt)
		printRun(out, r, price)
		return nil
	}

	suite := Suite{Models: map[string]string{}}
	for _, t := range tasks {
		m := models
		if offline {
			m = OfflineModels(t)
		}
		r := RunTask(ctx, t, m, opt)
		suite.Outcomes = append(suite.Outcomes, r.Outcome)
		for k, c := range m.Choices {
			suite.Models[k] = c.Model
		}
	}
	fmt.Fprint(out, suite.Markdown(price))
	return baselineStep(out, cfg, suite)
}

// baselineStep порівнює з базою або записує нову.
func baselineStep(out io.Writer, cfg config, suite Suite) error {
	if cfg.baseline == "" {
		return nil
	}
	if cfg.writeBase {
		if err := SaveBaseline(cfg.baseline, suite); err != nil {
			return err
		}
		fmt.Fprintf(out, "\nбазу записано: %s\n", cfg.baseline)
		return nil
	}
	base, err := LoadBaseline(cfg.baseline)
	if err != nil {
		return fmt.Errorf("%w (%s); створіть її: -write-baseline", err, cfg.baseline)
	}
	if reg := Regressions(base, suite); len(reg) > 0 {
		return fmt.Errorf("регресія на holdout: %s", strings.Join(reg, ", "))
	}
	fmt.Fprintln(out, "\nрегресій відносно бази немає")
	return nil
}

func printRun(out io.Writer, r Run, price *usage.Price) {
	fmt.Fprintln(out, "── слід спроб (session state, StateDelta) ──")
	for _, ev := range r.Events {
		for k, v := range ev.Actions.StateDelta {
			fmt.Fprintf(out, "  %s = %v\n", k, v)
		}
	}
	o := r.Outcome
	fmt.Fprintln(out, "── підсумок ──")
	fmt.Fprintf(out, "прийнято: %t, спроб: %d, раундів критиків: %d, час: %s\n",
		o.Accepted, o.Attempts, o.CriticRounds, time.Duration(o.ElapsedMS)*time.Millisecond)
	fmt.Fprintf(out, "holdout: %s\n", holdoutCell(o))
	if o.Err != "" {
		fmt.Fprintf(out, "помилка циклу: %s\n", o.Err)
	}
	fmt.Fprintf(out, "\nтокени (за UsageMetadata провайдера):\n%s", r.Meter.Table())
	if price != nil {
		fmt.Fprintf(out, "вартість за вказаним тарифом: $%.4f\n", price.Cost(o.Tokens))
	}
	if r.Code != "" {
		fmt.Fprintf(out, "\nприйнятий код:\n%s\n", r.Code)
	}
}

func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	return s
}
