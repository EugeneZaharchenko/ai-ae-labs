package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/ledgerkb"
	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/metric"
	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/sandbox"
)

type options struct {
	ask                              string
	selfImprove, evaluate, replay    bool
	evalDir, set, strategyPath, by   string
	experiments, maxTokens, parallel int
	offline                          bool
	window                           time.Duration
}

func parseOptions(args []string, out io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("research", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&o.ask, "ask", "", "питання: прогнати конвеєр із паузою для людини перед фінальним звітом")
	fs.BoolVar(&o.selfImprove, "self-improve", false, "цикл самополіпшення strategy.md")
	fs.BoolVar(&o.evaluate, "evaluate", false, "оцінити чинну стратегію на eval-наборі (--set)")
	fs.BoolVar(&o.replay, "replay", false, "перевірити цілісність experiments.log")
	fs.StringVar(&o.evalDir, "eval", "eval", "тека з dev.json і generalization.json")
	fs.StringVar(&o.set, "set", "dev", "для --evaluate: dev або generalization")
	fs.StringVar(&o.strategyPath, "strategy", "strategy.md", "шлях до strategy.md (його тека — корінь пісочниці)")
	fs.IntVar(&o.experiments, "experiments", 20, "жорстка стеля експериментів (0 — помилка, не «безмежно»)")
	fs.IntVar(&o.maxTokens, "max-tokens", 2_000_000, "стеля токенів за UsageMetadata (0 — без стелі)")
	fs.IntVar(&o.parallel, "parallel", 1, "++ Advanced: скільки кандидатів оцінювати паралельно (ізольовані worktree)")
	fs.BoolVar(&o.offline, "offline", false, "правила замість моделей навіть за наявності ключа")
	fs.StringVar(&o.by, "by", "human", "хто затверджує звіт у --ask (пишеться в approvals.log)")
	fs.DurationVar(&o.window, "window", 10*time.Minute, "скільки чекати рішення людини; тиша = відмова")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if filepath.Base(o.strategyPath) != sandbox.StrategyFile {
		return o, fmt.Errorf("--strategy має вказувати на файл %s", sandbox.StrategyFile)
	}
	if o.experiments < 1 || o.parallel < 1 {
		return o, errors.New("--experiments і --parallel мають бути ≥ 1")
	}
	return o, nil
}

// Models used by one run and the line that tells a human what they are.
type Models struct {
	Writer, Critic, Proposer model.LLM
	Live                     bool
	Label                    string
}

// resolveModels is a seam for tests: CLI tests must not touch the network.
var resolveModels = func(ctx context.Context, offline bool) Models {
	if offline {
		return offlineModels("запитано --offline")
	}
	_ = modelcfg.LoadEnv(".")
	m, choice, err := modelcfg.Load(ctx)
	if err != nil {
		return offlineModels(strings.SplitN(err.Error(), "\n", 2)[0])
	}
	return Models{Writer: m, Critic: m, Proposer: m, Live: true, Label: "LLM: " + choice.Reason}
}

func offlineModels(cause string) Models {
	return Models{
		Writer: offlineWriter{}, Critic: offlineCritic{},
		Proposer: fakellm.New("scripted-proposer (NOT an LLM)", offlineProposals()...),
		Label:    "НЕМАЄ МОДЕЛІ (" + cause + ") — " + OfflineName + "; доводить гальма й проводку, а не те, що LLM покращує стратегію",
	}
}

// offlineProposals: seven scripted steps that exercise every verdict —
// keeps, a refused candidate (two metrics), a regression and a tie.
func offlineProposals() []fakellm.Turn {
	s := BaselineStrategy()
	var turns []fakellm.Turn
	step := func(summary string, edit func(*Strategy)) {
		c := s
		edit(&c)
		c.Title = "Candidate: " + summary
		content := c.Render()
		if summary == "друга метрика (має бути refused)" {
			content = strings.Replace(content, "- facts_found_ratio", "- facts_found_ratio\n- readability", 1)
		} else if !strings.Contains(summary, "регресія") && !strings.Contains(summary, "порядок") {
			s = c
		}
		turns = append(turns,
			fakellm.CallTurn("write_strategy", map[string]any{"path": sandbox.StrategyFile, "content": content, "summary": summary}),
			fakellm.TextTurn("proposed"))
	}
	step("top_k 1→3: більше уривків на кожен підзапит", func(c *Strategy) { c.TopK = 3 })
	step("розбити питання на підзапити (max_subqueries 1→4)", func(c *Strategy) { c.MaxSubqueries = 4 })
	step("читати повний текст 2 найкращих документів (read_docs 0→2)", func(c *Strategy) { c.ReadDocs = 2 })
	step("додати graph_lookup як друге джерело", func(c *Strategy) { c.Sources = []string{SourceKB, SourceGraph} })
	step("друга метрика (має бути refused)", func(*Strategy) {})
	step("регресія: top_k назад до 1", func(c *Strategy) { c.TopK = 1 })
	step("порядок критиків: citations перед clarity", func(c *Strategy) { c.Critics = []string{CriticCitations, CriticClarity} })
	return turns
}

func run(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	o, err := parseOptions(args, out)
	if err != nil {
		return err
	}
	root := filepath.Dir(o.strategyPath)
	if o.replay {
		return replayLog(filepath.Join(root, "experiments.log"), out)
	}
	models := resolveModels(ctx, o.offline)
	fmt.Fprintln(out, "модель:", models.Label)
	dev, gen, err := metric.LoadDir(o.evalDir)
	if err != nil {
		return err
	}
	lib := NewLibrary(ledgerkb.New())
	switch {
	case o.selfImprove:
		return selfImprove(ctx, o, root, models, lib, dev, gen, out)
	case o.evaluate:
		set := dev
		if o.set == "generalization" {
			set = gen
		}
		return evaluate(ctx, root, models, lib, set, out)
	case o.ask != "":
		return ask(ctx, o, root, models, lib, in, out)
	}
	return errors.New("оберіть режим: --ask, --self-improve, --evaluate або --replay")
}

func loadStrategy(root string) (Strategy, error) {
	raw, err := sandbox.ReadFile(root, sandbox.StrategyFile)
	if err != nil {
		return Strategy{}, fmt.Errorf("створіть %s: %w", sandbox.StrategyFile, err)
	}
	return ParseStrategy(string(raw))
}

func readFileString(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// gitSHA is the code version an experiment ran with (unknown outside git).
func gitSHA(root string) string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = root
	b, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(b))
}

func selfImprove(ctx context.Context, o options, root string, models Models, lib *Library, dev, gen metric.Set, out io.Writer) error {
	start := time.Now()
	logPath := filepath.Join(root, "experiments.log")
	prev, err := ReadLogFile(logPath)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("experiments.log: %w", err)
	}
	defer f.Close()
	lastID := 0
	if len(prev) > 0 {
		lastID = prev[len(prev)-1].ID
	}
	all := append(append([]metric.Case{}, dev.Cases...), gen.Cases...)
	eng := &Engine{
		Root:    root,
		Budget:  Budget{MaxExperiments: o.experiments, PerExperiment: 15 * time.Minute, MaxTokens: o.maxTokens},
		Epsilon: 0.04,
		Guards:  []Guard{NoGoldenLeak(all)},
		Scorer:  &PipelineScorer{Lib: lib, Writer: models.Writer, Critic: models.Critic, Cases: dev.Cases, Parallel: 3},
		Audit:   NewAuditLog(f, lastID),
		GitSHA:  gitSHA(root),
	}
	printEntry := func(e Entry) {
		fmt.Fprintf(out, "  #%d %-8s %.3f → %.3f  eval=%d proposer=%d tok  %.0fs  %s\n      підстава: %s\n",
			e.ID, e.Verdict, e.PrevMetric, e.NewMetric, e.EvalTokens, e.ProposerTokens, e.DurationSec, e.DiffSummary, e.Reason)
	}
	var rep Report
	if o.parallel > 1 {
		rep, err = runParallel(ctx, eng, models, o.parallel, printEntry, out)
	} else {
		rep, err = runSequential(ctx, eng, models, o.experiments, printEntry, out)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nдев-метрика: %.3f → %.3f · експериментів %d (keep %d, discard %d, refused %d) · стоп: %s · токени %d · %s\n",
		rep.Baseline, rep.Best, len(rep.Entries), rep.Keeps, rep.Discards, rep.Refused, rep.Stop, rep.Tokens,
		time.Since(start).Round(time.Second))
	if len(gen.Cases) > 0 {
		gs := &PipelineScorer{Lib: lib, Writer: models.Writer, Critic: models.Critic, Cases: gen.Cases, Parallel: 3}
		if m, err := gs.Score(ctx, eng.Strategy()); err == nil {
			fmt.Fprintf(out, "generalization (питання, яких цикл не бачив) з чинною стратегією: %.3f\n", m.Score)
		} else {
			fmt.Fprintf(out, "generalization: не виміряно: %v\n", err)
		}
	}
	fmt.Fprintf(out, "чинна стратегія: %s; журнал: %s\n", filepath.Join(root, sandbox.StrategyFile), logPath)
	return nil
}

// runSequential drives the Engine with the ADK loopagent [proposer, evaluator].
func runSequential(ctx context.Context, eng *Engine, models Models, maxIter int, onEntry func(Entry), out io.Writer) (Report, error) {
	sink := NewProposalSink(eng.Root)
	eng.Proposer = sink
	proposer, err := NewProposerAgent(models.Proposer, sink, eng.Brief)
	if err != nil {
		return Report{}, err
	}
	var rep Report
	loop, err := NewSelfImproveAgent(eng, proposer, maxIter, func(e Entry) { rep.add(e); onEntry(e) })
	if err != nil {
		return Report{}, err
	}
	if err := eng.Start(ctx); err != nil {
		return Report{}, err
	}
	rep.Baseline = eng.CurrentScore()
	fmt.Fprintf(out, "базова метрика (dev): %.3f\n", rep.Baseline)
	if _, err := runAgent(ctx, loop, "improve the strategy"); err != nil {
		return rep, fmt.Errorf("цикл: %w", err)
	}
	rep.Best, rep.Tokens, rep.Stop = eng.CurrentScore(), eng.Tokens(), eng.Exhausted()
	return rep, nil
}

func evaluate(ctx context.Context, root string, models Models, lib *Library, set metric.Set, out io.Writer) error {
	s, err := loadStrategy(root)
	if err != nil {
		return err
	}
	sc := &PipelineScorer{Lib: lib, Writer: models.Writer, Critic: models.Critic, Cases: set.Cases, Parallel: 3}
	start := time.Now()
	results := sc.Run(ctx, s)
	fmt.Fprintf(out, "стратегія: %s · набір: %s (%d питань) · %s\n\n", s.Title, set.Name, len(set.Cases), time.Since(start).Round(time.Second))
	fmt.Fprintln(out, "| питання | факти | джерела | цитовані документи | критики | токени |")
	fmt.Fprintln(out, "|---|---|---|---|---|---|")
	found, total := 0, 0
	var answers []string
	for _, r := range results {
		status := fmt.Sprintf("%d/%d", r.Found, r.Total)
		if r.TimedOut {
			status += " (timeout)"
		} else if r.Failure != nil {
			status += " (помилка)"
		}
		fmt.Fprintf(out, "| %s | %s | %s | %s | %s, %d раунд. | %d |\n", r.ID, status, strings.Join(r.Sources, "+"),
			strings.Join(r.Cited, " "), passedWord(r.CriticsPassed), r.Rounds, r.Tokens)
		found += r.Found
		total += r.Total
		answers = append(answers, r.Report)
	}
	fmt.Fprintf(out, "\nметрика: %d/%d = %.3f\n\nза типами фактів:\n", found, total, metric.Ratio(found, total))
	byType := metric.ByType(set.Cases, answers)
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		fmt.Fprintf(out, "  %-9s %d/%d\n", t, byType[t].Found, byType[t].Total)
	}
	return nil
}

func replayLog(path string, out io.Writer) error {
	entries, err := ReadLogFile(path)
	if err != nil {
		return err
	}
	st, err := Replay(entries)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "журнал цілісний: %d записів; keep %d, discard %d, refused %d, proposer_failed %d; остання метрика %.3f\n",
		len(entries), st.Keeps, st.Discards, st.Refused, st.Failures, st.Final)
	return nil
}

// readLine reads one reply from in within the window; silence is a refusal.
func readLine(ctx context.Context, in io.Reader, window time.Duration) (string, error) {
	ch := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(in)
		if sc.Scan() {
			ch <- sc.Text()
		}
		close(ch)
	}()
	t := time.NewTimer(window)
	defer t.Stop()
	select {
	case line, ok := <-ch:
		if !ok {
			return "", errors.New("вхід закрито без відповіді")
		}
		return line, nil
	case <-t.C:
		return "", fmt.Errorf("ніхто не відповів за %s", window)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
