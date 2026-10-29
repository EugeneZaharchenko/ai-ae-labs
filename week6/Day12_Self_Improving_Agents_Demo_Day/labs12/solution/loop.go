// Збірка живого циклу: вибір моделі, Engine, loopagent і підсумок.
//
// Тут немає нової логіки циклу — вона в improve.go. Цей файл відповідає на
// питання «чим саме ми це запускаємо»: реальна модель із internal/modelcfg або,
// якщо жодного провайдера не налаштовано, офлайн-замінник, який друкує про себе
// правду. Мовчазної підміни моделі тут немає.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/ledgerkb"
	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/offline"
)

// Models — пара моделей циклу й підпис, який бачить людина.
type Models struct {
	// Worker відповідає на питання eval-набору; Proposer пише нову стратегію.
	// Це дві різні ролі: пропонент бачить поведінку воркера, але не золоті факти.
	Worker   model.LLM
	Proposer model.LLM
	// Live — true, лише якщо обидві ролі виконує справжня LLM.
	Live bool
	// Label — один рядок для консолі й аудиту.
	Label string
}

// ResolveModels обирає модель через internal/modelcfg.
//
// Якщо провайдера немає, повертає офлайн-пару й ПРИЧИНУ в Label: прогін без
// ключа лишається корисним (він доводить гальма й проводку), але він ніколи не
// видає себе за доказ того, що LLM покращує власну стратегію.
func ResolveModels(ctx context.Context, rel string) Models {
	if err := modelcfg.LoadEnv("."); err != nil {
		return offlineModels("apps/.env не прочитано: "+err.Error(), rel)
	}
	m, choice, err := modelcfg.Load(ctx)
	if err != nil {
		return offlineModels(strings.SplitN(err.Error(), "\n", 2)[0], rel)
	}
	return Models{Worker: m, Proposer: m, Live: true, Label: "LLM: " + choice.Reason}
}

func offlineModels(cause, rel string) Models {
	return Models{
		Worker:   offline.Worker{},
		Proposer: fakellm.New("scripted-proposer (NOT an LLM)", offlineCandidates(rel)...),
		Label: "НЕМАЄ ПРОВАЙДЕРА (" + cause + ") — " + offline.Name +
			"; прогін доводить гальма й проводку, а не те, що LLM покращує стратегію",
	}
}

// offlineCandidates — те, що «пропонує» офлайн-пропонент: два заздалегідь
// написані кроки, ті самі важелі, на які реагує offline.Worker.
func offlineCandidates(rel string) []fakellm.Turn {
	step := func(instr, summary string) []fakellm.Turn {
		doc := baselineStrategy()
		doc.Title = "Candidate: " + summary
		doc.Tools = []string{ledgerkb.ToolSearch, ledgerkb.ToolRead}
		doc.Instruction = instr
		return []fakellm.Turn{
			fakellm.CallTurn("write_strategy", map[string]any{
				"path": rel, "content": RenderStrategy(doc), "summary": summary,
			}),
			fakellm.TextTurn("proposed"),
		}
	}
	var turns []fakellm.Turn
	turns = append(turns, step(
		"Search, then open the best document with kb_read before answering (the snippet is only its scope paragraph) and quote every figure you find.",
		"додати kb_read і цитувати кожну цифру")...)
	turns = append(turns, step(
		"Search, open every relevant document with kb_read, and quote every figure you find in your answer.",
		"читати кілька документів, а не один")...)
	return turns
}

// LoopConfig — параметри одного прогону.
type LoopConfig struct {
	Root         string
	StrategyName string
	LogName      string
	Experiments  int
	MaxTokens    int
	// ReportOnly — L1: цикл працює над одноразовою копією стратегії.
	ReportOnly bool
	// Heldout — виміряти ще й відкладений набір до/після (не впливає на keep).
	Heldout bool
	Models  Models
}

// Summary — підсумок для консолі.
type Summary struct {
	Baseline, Final       float64
	HeldoutBefore         float64
	HeldoutAfter          float64
	HeldoutMeasured       bool
	Entries               []Entry
	Tokens, HeldoutTokens int
}

// Dev і Heldout повертають eval-набори. Вони живуть у коді, а не у файлі, який
// агент міг би змінити: evaluator immutable.
func devCases() []ledgerkb.Case     { return ledgerkb.Dev() }
func heldoutCases() []ledgerkb.Case { return ledgerkb.Heldout() }

// RunLoop виконує цикл самополіпшення: loopagent[proposer, evaluator].
func RunLoop(ctx context.Context, cfg LoopConfig, out io.Writer) (Summary, error) {
	absRoot, strategyPath, err := prepareStrategy(cfg, out)
	if err != nil {
		return Summary{}, err
	}
	workDir, cleanup, err := workingDir(cfg, absRoot, strategyPath)
	if err != nil {
		return Summary{}, err
	}
	defer cleanup()

	surface, err := NewSurface(workDir, cfg.StrategyName)
	if err != nil {
		return Summary{}, fmt.Errorf("поверхня змін: %w", err)
	}
	kb := ledgerkb.New()
	worker := &LLMScorer{Model: cfg.Models.Worker, KB: kb, Cases: devCases(), Parallel: 4}

	var buffered strings.Builder
	allCases := append(devCases(), heldoutCases()...)
	eng := &Engine{
		Surface:   surface,
		Allowlist: ledgerkb.AllToolNames(),
		Budget: Budget{
			MaxExperiments: cfg.Experiments,
			PerExperiment:  10 * time.Minute,
			MaxTokens:      cfg.MaxTokens,
		},
		Epsilon: 0.04,
		Guards:  []Guard{NoGoldenLeak(allCases)},
		Scorer:  worker,
		Audit:   NewAuditLog(&buffered),
	}
	sink := NewProposalSink()
	eng.Proposer = sink

	proposer, err := NewProposerAgent(cfg.Models.Proposer, surface, sink, eng.Brief)
	if err != nil {
		return Summary{}, err
	}
	loop, err := NewSelfImproveAgent(eng, proposer, cfg.Experiments)
	if err != nil {
		return Summary{}, err
	}

	if err := eng.Start(ctx); err != nil {
		return Summary{}, err
	}
	sum := Summary{Baseline: eng.CurrentScore()}
	baselineDoc := eng.Strategy()
	fmt.Fprintf(out, "базова метрика (dev, %d фактів): %.3f\n", ledgerkb.TotalFacts(devCases()), sum.Baseline)

	if _, err := labrun.Run(ctx, loop, "improve the strategy"); err != nil {
		return sum, fmt.Errorf("цикл: %w", err)
	}

	entries, err := ReadEntries(strings.NewReader(buffered.String()))
	if err != nil {
		return sum, err
	}
	sum.Entries, sum.Final, sum.Tokens = entries, eng.CurrentScore(), eng.Tokens()

	if cfg.Heldout && eng.Strategy().Instruction != baselineDoc.Instruction {
		hs := &LLMScorer{Model: cfg.Models.Worker, KB: kb, Cases: heldoutCases(), Parallel: 4}
		before, errB := hs.Score(ctx, baselineDoc)
		after, errA := hs.Score(ctx, eng.Strategy())
		sum.HeldoutTokens = before.Tokens + after.Tokens
		if errB == nil && errA == nil {
			sum.HeldoutBefore, sum.HeldoutAfter, sum.HeldoutMeasured = before.Score, after.Score, true
		}
	}

	if err := persistLog(cfg, absRoot, entries); err != nil {
		return sum, err
	}
	return sum, nil
}

// prepareStrategy гарантує, що в робочій теці є strategy.md (базова стратегія).
func prepareStrategy(cfg LoopConfig, out io.Writer) (absRoot, strategyPath string, err error) {
	absRoot, err = absDir(cfg.Root)
	if err != nil {
		return "", "", err
	}
	strategyPath = absRoot + "/" + cfg.StrategyName
	seeded, err := ensureStrategy(strategyPath)
	if err != nil {
		return "", "", err
	}
	if seeded {
		fmt.Fprintf(out, "створено базовий %s (його не було)\n", cfg.StrategyName)
	}
	return absRoot, strategyPath, nil
}

func absDir(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("шлях до робочої теки: %w", err)
	}
	return abs, nil
}

// workingDir повертає теку, над якою працює Surface.
//
// У L1 це одноразова копія стратегії: гарантія «нічого не змінено» тримається
// конструкцією, а не дисципліною виклику.
func workingDir(cfg LoopConfig, absRoot, strategyPath string) (string, func(), error) {
	if !cfg.ReportOnly {
		return absRoot, func() {}, nil
	}
	original, err := os.ReadFile(strategyPath)
	if err != nil {
		return "", nil, fmt.Errorf("читання %s: %w", cfg.StrategyName, err)
	}
	sandbox, err := os.MkdirTemp("", "l1-report-only-")
	if err != nil {
		return "", nil, fmt.Errorf("тимчасова тека для L1: %w", err)
	}
	if err := os.WriteFile(filepath.Join(sandbox, cfg.StrategyName), original, 0o600); err != nil {
		os.RemoveAll(sandbox)
		return "", nil, fmt.Errorf("копія стратегії в пісочницю: %w", err)
	}
	return sandbox, func() { os.RemoveAll(sandbox) }, nil
}

// persistLog дописує записи прогону в справжній журнал, продовжуючи наскрізну
// нумерацію: інваріант цілісності належить файлу, а не процесу. У L1 вердикти
// перемарковуються на would_*.
func persistLog(cfg LoopConfig, absRoot string, entries []Entry) error {
	logPath := filepath.Join(absRoot, cfg.LogName)
	lastID, err := lastEntryID(logPath)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("відкриття %s: %w", cfg.LogName, err)
	}
	defer f.Close()
	log := NewAuditLogAfter(f, lastID)
	for _, e := range entries {
		if cfg.ReportOnly {
			e.Verdict = AsDryRun(e.Verdict)
		}
		if _, err := log.Append(e); err != nil {
			return err
		}
	}
	return nil
}
