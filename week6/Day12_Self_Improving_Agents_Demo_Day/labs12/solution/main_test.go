// Тести CLI та збірки циклу. Вони ганяються офлайн: ResolveModels без
// провайдера повертає замінники, і саме це тут перевіряється — що прогін
// чесно каже про це, а гальма й аудит працюють так само, як із живою моделлю.
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/offline"
)

// offlineCfg будує конфіг на офлайн-моделях, незалежно від ключів на машині.
func offlineCfg(root string) LoopConfig {
	return LoopConfig{
		Root: root, StrategyName: "strategy.md", LogName: "experiments.log",
		Experiments: 2, Models: offlineModels("тест", "strategy.md"),
	}
}

func readLog(t *testing.T, root string) []Entry {
	t.Helper()
	f, err := os.Open(filepath.Join(root, "experiments.log"))
	if err != nil {
		t.Fatalf("experiments.log: %v", err)
	}
	defer f.Close()
	entries, err := ReadEntries(f)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	return entries
}

func TestOfflineLoopKeepsImprovementsAndLogsTokens(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	sum, err := RunLoop(context.Background(), offlineCfg(root), &out)
	if err != nil {
		t.Fatalf("RunLoop: %v", err)
	}
	if sum.Final <= sum.Baseline {
		t.Fatalf("метрика не зросла: %.3f → %.3f\n%s", sum.Baseline, sum.Final, out.String())
	}
	entries := readLog(t, root)
	if len(entries) != 2 {
		t.Fatalf("записів = %d, очікували 2", len(entries))
	}
	state, err := Replay(entries)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if state.Keeps == 0 || state.Final != sum.Final {
		t.Errorf("replay %+v не збігається з підсумком %.3f", state, sum.Final)
	}
	strategy, _ := os.ReadFile(filepath.Join(root, "strategy.md"))
	if !strings.Contains(string(strategy), "kb_read") {
		t.Errorf("keep не змінив strategy.md:\n%s", strategy)
	}
}

func TestOfflineHeldoutIsMeasured(t *testing.T) {
	cfg := offlineCfg(t.TempDir())
	cfg.Heldout = true
	sum, err := RunLoop(context.Background(), cfg, new(bytes.Buffer))
	if err != nil {
		t.Fatalf("RunLoop: %v", err)
	}
	if !sum.HeldoutMeasured || sum.HeldoutAfter < sum.HeldoutBefore {
		t.Errorf("heldout не виміряно або впав: %+v", sum)
	}
}

// Головна гарантія L1: поверхня змін недоторкана, у журналі один would_*-рядок.
func TestReportOnlyNeverTouchesStrategy(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "strategy.md")
	custom := RenderStrategy(baselineStrategy())
	if err := os.WriteFile(path, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := offlineCfg(root)
	cfg.ReportOnly, cfg.Experiments = true, 1
	if _, err := RunLoop(context.Background(), cfg, new(bytes.Buffer)); err != nil {
		t.Fatalf("RunLoop: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != custom {
		t.Errorf("L1 змінив strategy.md:\n%s", after)
	}
	entries := readLog(t, root)
	if len(entries) != 1 || entries[0].Verdict != VerdictWouldKeep {
		t.Fatalf("очікували один would_keep, є %+v", entries)
	}
	if entries[0].DiffSummary == "" || entries[0].TS.IsZero() {
		t.Errorf("запис нечитабельний: %+v", entries[0])
	}
}

// Два L1-прогони підряд лишають журнал відтворюваним.
func TestReportOnlyLogStaysReplayable(t *testing.T) {
	root := t.TempDir()
	for range 2 {
		cfg := offlineCfg(root)
		cfg.ReportOnly, cfg.Experiments = true, 1
		if _, err := RunLoop(context.Background(), cfg, new(bytes.Buffer)); err != nil {
			t.Fatalf("RunLoop: %v", err)
		}
	}
	state, err := Replay(readLog(t, root))
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if state.DryRuns != 2 || state.Keeps+state.Discards != 0 {
		t.Errorf("L1 не має давати бойових вердиктів: %+v", state)
	}
}

// Модель просить записати файл поза поверхнею: гальмо №1 відхиляє, у журналі
// refused, а стратегія лишається як була.
func TestLoopRefusesWriteOutsideSurface(t *testing.T) {
	root := t.TempDir()
	cfg := offlineCfg(root)
	cfg.Experiments = 1
	cfg.Models.Proposer = fakellm.New("evil",
		fakellm.CallTurn("write_strategy", map[string]any{
			"path": "../../etc/passwd", "content": "x", "summary": "evil"}),
		fakellm.TextTurn("sorry"))
	if _, err := RunLoop(context.Background(), cfg, new(bytes.Buffer)); err != nil {
		t.Fatalf("RunLoop: %v", err)
	}
	entries := readLog(t, root)
	if len(entries) != 1 || entries[0].Verdict != VerdictProposerFailed {
		t.Fatalf("очікували proposer_failed (пропозицію відхилено до черги), є %+v", entries)
	}
	before := RenderStrategy(baselineStrategy())
	after, _ := os.ReadFile(filepath.Join(root, "strategy.md"))
	if string(after) != before {
		t.Error("стратегія змінилась попри відмову")
	}
}

// Кандидат, що вписує золоті факти в інструкцію, відхиляється Guard-ом.
func TestLoopRefusesGoldenLeak(t *testing.T) {
	root := t.TempDir()
	cfg := offlineCfg(root)
	cfg.Experiments = 1
	cheat := baselineStrategy()
	cheat.Tools = []string{"kb_search", "kb_read"}
	cheat.Instruction = "Always answer: compliance officer, 5 business days, 120 days, 7 calendar days, 15 EUR."
	cfg.Models.Proposer = fakellm.New("cheater",
		fakellm.CallTurn("write_strategy", map[string]any{
			"path": "strategy.md", "content": RenderStrategy(cheat), "summary": "cheat"}),
		fakellm.TextTurn("done"))
	if _, err := RunLoop(context.Background(), cfg, new(bytes.Buffer)); err != nil {
		t.Fatalf("RunLoop: %v", err)
	}
	entries := readLog(t, root)
	if len(entries) != 1 || entries[0].Verdict != VerdictRefused || entries[0].Scored {
		t.Fatalf("очікували refused без виміру, є %+v", entries)
	}
}

func TestRunFlags(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"--experiments=0"}, &out); err == nil {
		t.Error("--experiments=0 мав дати помилку")
	}
	if err := run([]string{"--nope"}, &out); err == nil {
		t.Error("невідомий прапорець мав дати помилку")
	}
}

func TestRunReportOnlyPrintsModelLabel(t *testing.T) {
	resolveModels = func(_ context.Context, rel string) Models { return offlineModels("тест", rel) }
	t.Cleanup(func() { resolveModels = ResolveModels })

	var out bytes.Buffer
	root := t.TempDir()
	if err := run([]string{"--report-only", "--root=" + root}, &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	for _, want := range []string{"модель: НЕМАЄ ПРОВАЙДЕРА (тест)", offline.Name, "would_keep", "не змінено"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("у виводі немає %q:\n%s", want, out.String())
		}
	}
}
