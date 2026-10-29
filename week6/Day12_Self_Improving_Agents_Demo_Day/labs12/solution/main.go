// CLI циклу самополіпшення за патерном karpathy/autoresearch — на ADK Go.
//
// Що тут реально: справжня LLM (internal/modelcfg) і як воркер, що відповідає на
// питання eval-набору через function-calling інструменти kb_search/kb_read, і як
// пропонент, що пише нову версію стратегії викликом write_strategy. Метрика —
// чиста функція від тексту відповіді (частка знайдених фактів), без судді-LLM.
//
//	go run ./solution --report-only      # L1: один експеримент над копією, нічого не змінено
//	go run ./solution --experiments=3    # L2-подібний прогін: strategy.md змінюється при keep
//	go run ./solution web webui api      # живий Web UI воркера (див. web.go)
//
// Без жодного налаштованого провайдера прогін іде на офлайн-замінниках і
// першим рядком каже, чому. Такий прогін доводить гальма й проводку, але НЕ
// доводить, що LLM покращує власну стратегію.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/ledgerkb"
)

// baselineStrategy — нульовий стан: свідомо слабка стратегія («один пошук, одне
// речення зі сніпета»). Сніпет у kb_search — лише вступний абзац документа, а
// цифри лежать глибше, тому ця стратегія гарантовано недобирає факти й є що
// покращувати.
func baselineStrategy() StrategyDoc {
	return StrategyDoc{
		Title:       "Baseline strategy: search once, answer from snippets",
		Metric:      "facts_found_ratio",
		Tools:       []string{ledgerkb.ToolSearch},
		Instruction: "Search the knowledge base once and answer in one sentence from the search results.",
		MaxAttempts: 6,
	}
}

// resolveModels — шов для тестів: CLI-тест не повинен ходити в мережу.
var resolveModels = ResolveModels

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "помилка:", err)
		os.Exit(1)
	}
}

// run винесено з main, щоб цей самий шлях проганяли тести без os.Exit.
func run(args []string, out io.Writer) error {
	if isServeCmd(args) {
		return serve(context.Background(), args, out)
	}
	fs := flag.NewFlagSet("selfimprove", flag.ContinueOnError)
	fs.SetOutput(out)
	reportOnly := fs.Bool("report-only", false,
		"L1: виміряти кандидата й записати вердикт would_*, нічого не змінюючи")
	root := fs.String("root", ".", "робоча тека: тут лежать strategy.md і experiments.log")
	strategyName := fs.String("strategy", "strategy.md", "ім'я артефакту стратегії всередині --root")
	logName := fs.String("log", "experiments.log", "ім'я аудиторського журналу всередині --root")
	experiments := fs.Int("experiments", 3, "жорстка стеля експериментів (0 — помилка, не «безмежно»)")
	maxTokens := fs.Int("max-tokens", 400000, "сумарна стеля токенів за UsageMetadata (0 — без стелі)")
	heldout := fs.Bool("heldout", true, "виміряти відкладений набір до/після (не впливає на keep)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *experiments < 1 {
		return errors.New("--experiments має бути ≥ 1")
	}

	ctx := context.Background()
	models := resolveModels(ctx, *strategyName)
	fmt.Fprintln(out, "модель:", models.Label)

	cfg := LoopConfig{
		Root: *root, StrategyName: *strategyName, LogName: *logName,
		Experiments: *experiments, MaxTokens: *maxTokens,
		ReportOnly: *reportOnly, Heldout: *heldout && !*reportOnly, Models: models,
	}
	if cfg.ReportOnly {
		cfg.Experiments = 1
	}
	start := time.Now()
	sum, err := RunLoop(ctx, cfg, out)
	if err != nil {
		return err
	}
	printSummary(out, cfg, sum, time.Since(start))
	return nil
}

func printSummary(out io.Writer, cfg LoopConfig, sum Summary, took time.Duration) {
	mode := "прогін"
	if cfg.ReportOnly {
		mode = "L1 report-only"
	}
	fmt.Fprintf(out, "\n%s · %d експеримент(ів) · %s\n", mode, len(sum.Entries), took.Round(time.Second))
	for _, e := range sum.Entries {
		v := e.Verdict
		if cfg.ReportOnly {
			v = AsDryRun(v)
		}
		fmt.Fprintf(out, "  #%d %-13s %.3f → %.3f  eval=%d proposer=%d tok  %s\n",
			e.ID, v, e.PrevScore, e.NewScore, e.EvalTokens, e.ProposerTokens, e.DiffSummary)
		fmt.Fprintf(out, "      підстава: %s\n", e.Reason)
	}
	fmt.Fprintf(out, "  dev-метрика       : %.3f → %.3f\n", sum.Baseline, sum.Final)
	if sum.HeldoutMeasured {
		fmt.Fprintf(out, "  heldout (не бачив пропонент): %.3f → %.3f\n", sum.HeldoutBefore, sum.HeldoutAfter)
	}
	fmt.Fprintf(out, "  токени циклу      : %d (+%d heldout)\n", sum.Tokens, sum.HeldoutTokens)
	if cfg.ReportOnly {
		fmt.Fprintf(out, "  %s не змінено; дописано рядок у %s\n", cfg.StrategyName, cfg.LogName)
	} else {
		fmt.Fprintf(out, "  чинна стратегія: %s; журнал: %s\n", filepath.Join(cfg.Root, cfg.StrategyName), cfg.LogName)
	}
}

// lastEntryID повертає ID останнього запису в журналі (0, якщо журналу ще
// немає). Читаємо через ReadEntries, а не рахуємо рядки: пошкоджений журнал має
// зупинити прогін тут, а не дати мовчазний неправильний номер.
func lastEntryID(path string) (int, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("читання %s: %w", filepath.Base(path), err)
	}
	defer f.Close()

	entries, err := ReadEntries(f)
	if err != nil {
		return 0, fmt.Errorf("журнал %s пошкоджено: %w", filepath.Base(path), err)
	}
	if len(entries) == 0 {
		return 0, nil
	}
	return entries[len(entries)-1].ID, nil
}

// ensureStrategy створює базовий strategy.md, якщо його ще немає.
// Повертає true, якщо файл було створено цим викликом.
func ensureStrategy(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("перевірка %s: %w", filepath.Base(path), err)
	}
	if err := os.WriteFile(path, []byte(RenderStrategy(baselineStrategy())), 0o600); err != nil {
		return false, fmt.Errorf("створення %s: %w", filepath.Base(path), err)
	}
	return true, nil
}
