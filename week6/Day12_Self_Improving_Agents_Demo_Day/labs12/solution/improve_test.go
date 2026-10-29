// Тести Engine напряму, без CLI/L1-обв'язки — F10: max_attempts зі стратегії
// має зупиняти цикл раніше за зовнішній Budget, а провал Proposer має лишати
// слід в experiments.log, а не мовчки зникати.
package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
)

// newTestSurface готує тимчасову поверхню з базовою стратегією на диску.
func newTestSurface(t *testing.T, doc StrategyDoc) *Surface {
	t.Helper()
	root := t.TempDir()
	surface, err := NewSurface(root, "strategy.md")
	if err != nil {
		t.Fatalf("NewSurface: %v", err)
	}
	if err := surface.Write([]byte(RenderStrategy(doc))); err != nil {
		t.Fatalf("посів базової стратегії: %v", err)
	}
	return surface
}

// tieProposer щоразу пропонує ту саму стратегію без змін — кожна ітерація
// закінчується нічиєю (delta = 0) і, отже, discard.
type tieProposer struct{ rel string }

func (p tieProposer) Propose(_ context.Context, current StrategyDoc, _ []Entry) (Proposal, error) {
	return Proposal{Path: p.rel, Content: RenderStrategy(current), Summary: "нічия: та сама стратегія"}, nil
}

// failingProposer завжди повертає помилку — модель впала або вичерпалась.
type failingProposer struct{ err error }

func (p failingProposer) Propose(context.Context, StrategyDoc, []Entry) (Proposal, error) {
	return Proposal{}, p.err
}

func TestEngine_StrategyMaxAttemptsStopsBeforeBudget(t *testing.T) {
	doc := StrategyDoc{
		Title:       "Baseline",
		Metric:      "composite_score",
		Tools:       []string{"kb_search"},
		Instruction: "Search once.",
		MaxAttempts: 3,
	}
	surface := newTestSurface(t, doc)

	var buffered bytes.Buffer
	eng := &Engine{
		Surface:   surface,
		Allowlist: []string{"kb_search", "kb_read"},
		// Бюджет навмисно щедрий — 10 — щоб перевірити, що зупиняє цикл
		// саме MaxAttempts стратегії (3), а не Budget.
		Budget:   Budget{MaxExperiments: 10, PerExperiment: time.Second},
		Epsilon:  0.5,
		Proposer: tieProposer{rel: surface.Rel()},
		Scorer:   &FixtureScorer{Default: 1000},
		Audit:    NewAuditLog(&buffered),
		Now:      func() time.Time { return time.Time{} },
	}

	rep, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Stop != StopStrategyMaxAttempts {
		t.Fatalf("Stop = %q, хотіли %q (Budget.MaxExperiments=10 не мав спрацювати раніше)",
			rep.Stop, StopStrategyMaxAttempts)
	}
	if rep.Experiments != doc.MaxAttempts {
		t.Errorf("Experiments = %d, хотіли рівно %d (межа стратегії, а не бюджету)",
			rep.Experiments, doc.MaxAttempts)
	}
	if rep.Discards != doc.MaxAttempts {
		t.Errorf("Discards = %d, хотіли %d: кожна спроба — нічия", rep.Discards, doc.MaxAttempts)
	}
}

func TestEngine_ProposerFailureIsLogged(t *testing.T) {
	doc := StrategyDoc{
		Title:       "Baseline",
		Metric:      "composite_score",
		Tools:       []string{"kb_search"},
		Instruction: "Search once.",
		MaxAttempts: 5,
	}
	surface := newTestSurface(t, doc)

	var buffered bytes.Buffer
	wantErr := errors.New("модель вичерпала квоту")
	eng := &Engine{
		Surface:   surface,
		Allowlist: []string{"kb_search", "kb_read"},
		Budget:    Budget{MaxExperiments: 5, PerExperiment: time.Second},
		Epsilon:   0.5,
		Proposer:  failingProposer{err: wantErr},
		Scorer:    &FixtureScorer{Default: 1000},
		Audit:     NewAuditLog(&buffered),
		Now:       func() time.Time { return time.Time{} },
	}

	rep, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Stop != StopProposerFailed {
		t.Fatalf("Stop = %q, хотіли %q", rep.Stop, StopProposerFailed)
	}

	// Головна перевірка F10: провал Proposer не має бути мовчазним — він
	// мусить лишити рівно один запис в experiments.log, інакше журнал
	// закінчується на N-1 записі без пояснення, чому.
	entries, err := ReadEntries(&buffered)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("записів у журналі = %d, хотіли рівно 1 (провал Proposer теж пишеться)", len(entries))
	}
	got := entries[0]
	if got.Verdict != VerdictProposerFailed {
		t.Errorf("Verdict = %q, хотіли %q", got.Verdict, VerdictProposerFailed)
	}
	if got.Reason != wantErr.Error() {
		t.Errorf("Reason = %q, хотіли %q", got.Reason, wantErr.Error())
	}
	if got.NewScore != got.PrevScore {
		t.Errorf("NewScore/PrevScore = %v/%v, хотіли рівні: провал не рухає ланцюжок метрик",
			got.NewScore, got.PrevScore)
	}

	replayed, err := Replay(entries)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if replayed.ProposerFailures != 1 {
		t.Errorf("ProposerFailures = %d, хотіли 1", replayed.ProposerFailures)
	}
	if replayed.Keeps != 0 || replayed.Discards != 0 {
		t.Errorf("keeps=%d discards=%d, хотіли обидва 0", replayed.Keeps, replayed.Discards)
	}
}

// runProposer проганяє агента-пропонента один раз через ADK-раннер.
func runProposer(t *testing.T, a agent.Agent) {
	t.Helper()
	if _, err := labrun.Run(context.Background(), a, "propose"); err != nil {
		t.Fatalf("proposer run: %v", err)
	}
}
