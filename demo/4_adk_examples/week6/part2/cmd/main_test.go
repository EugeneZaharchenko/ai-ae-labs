package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two contours. The pure layer — the surface, the schema, the metric, the loop's
// keep/discard policy over fake Measure and Propose — needs no ADK and no model.
// The CLI runs go through real llmagents, real tools and a real session with the
// offline stand-ins, and pin what only a run can show: that giving the worker
// kb_read changes what it answers, and that the brakes fire.

func newSurface(t *testing.T) (*Surface, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewSurface(dir, DefaultPath)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestSurfaceAllowsExactlyOneFile(t *testing.T) {
	s, dir := newSurface(t)
	if err := s.Write("strategy.md", "x"); err != nil {
		t.Fatalf("the declared file must be writable: %v", err)
	}
	if err := s.Write("./sub/../strategy.md", "y"); err != nil {
		t.Errorf("a path that cleans to the declared file is that file: %v", err)
	}
	for name, p := range map[string]string{
		"traversal": "../escape.md", "other file": "notes.md", "nested": "sub/strategy.md",
		"absolute": filepath.Join(dir, "strategy.md"), "empty": "",
	} {
		if err := s.Write(p, "z"); !errors.Is(err, ErrOutsideSurface) {
			t.Errorf("%s (%q): err = %v, want ErrOutsideSurface", name, p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.md")); err == nil {
		t.Error("a rejected write must leave nothing behind")
	}
}

func TestSurfaceRefusesASymlinkedStrategyFile(t *testing.T) {
	s, dir := newSurface(t)
	outside := filepath.Join(t.TempDir(), "victim.md")
	if err := os.WriteFile(outside, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, DefaultPath)); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	// The name is right and a prefix check would pass. The write would land
	// outside the directory.
	if err := s.Write(DefaultPath, "overwritten"); !errors.Is(err, ErrOutsideSurface) {
		t.Fatalf("err = %v, want ErrOutsideSurface", err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "original" {
		t.Errorf("the symlink target was written: %q", b)
	}
}

func TestStrategySchema(t *testing.T) {
	rt, err := ParseStrategy(Baseline().Render())
	if err != nil || rt.Validate() != nil || rt.Instruction != Baseline().Instruction {
		t.Fatalf("baseline must round-trip: %+v, %v", rt, err)
	}
	for name, s := range map[string]Strategy{
		"unknown tool": {Title: "t", Tools: []string{"shell"}, Instruction: "x"},
		"no tools":     {Title: "t", Instruction: "x"},
		"empty":        {Title: "t", Tools: []string{ToolSearch}},
		"too long":     {Title: "t", Tools: []string{ToolSearch}, Instruction: strings.Repeat("x", maxInstructionRunes+1)},
	} {
		if err := s.Validate(); !errors.Is(err, ErrInvalidStrategy) {
			t.Errorf("%s: err = %v, want ErrInvalidStrategy", name, err)
		}
	}
	if _, err := ParseStrategy("just prose"); !errors.Is(err, ErrInvalidStrategy) {
		t.Errorf("prose is not a strategy: %v", err)
	}
	path, s, err := ParseProposal("```\npath: ../x.md\n# T\ntools: kb_search\n\nDo it.\n```")
	if err != nil || path != "../x.md" || s.Title != "T" {
		t.Errorf("proposal with a path line: %q %+v %v", path, s, err)
	}
}

func TestMetricAndKBInvariants(t *testing.T) {
	kb := NewKB()
	for _, c := range Dev() {
		hits := kb.Search(c.Question)
		if len(hits) == 0 {
			t.Fatalf("%s: search found nothing", c.ID)
		}
		// Search must NOT leak the answer, or the lever the loop is meant to find
		// does not exist and the experiment measures nothing.
		var snippets []string
		for _, h := range hits {
			snippets = append(snippets, h.Snippet)
		}
		if f, _ := c.Found(strings.Join(snippets, " ")); f != 0 {
			t.Errorf("%s: search snippets already contain %d golden facts", c.ID, f)
		}
		// And the top hit's full text must contain them all, or no strategy can win.
		text, _ := kb.Read(hits[0].ID)
		if f, missed := c.Found(text); f != len(c.Facts) {
			t.Errorf("%s: top document %q lacks %v", c.ID, hits[0].ID, missed)
		}
	}
	if kb.Reads() != len(Dev()) {
		t.Errorf("Reads = %d, want one per question", kb.Reads())
	}
}

// fakeEnv scores strategies by a table keyed on the strategy's title.
func fakeEnv(scores map[string]float64, proposals ...string) Env {
	i := 0
	return Env{
		Measure: func(_ context.Context, s Strategy) (Measurement, error) {
			return Measurement{Found: int(scores[s.Title] * 100), Total: 100}, nil
		},
		Propose: func(context.Context, Strategy, Measurement) (string, error) {
			p := proposals[i%len(proposals)]
			i++
			return p, nil
		},
	}
}

func proposal(title, tools string) string {
	return "# " + title + "\ntools: " + tools + "\n\nDo the thing.\n"
}

func TestImproveKeepsDiscardsAndLogsEverything(t *testing.T) {
	s, dir := newSurface(t)
	env := fakeEnv(
		map[string]float64{"Baseline": 0.2, "win": 0.6, "worse": 0.4, "same": 0.6, "tiny": 0.62},
		proposal("win", "kb_search, kb_read"),
		"path: ../escape.md\n"+proposal("win", "kb_search"),
		proposal("bad", "shell"),
		proposal("worse", "kb_search"),
		proposal("same", "kb_search"),
		proposal("tiny", "kb_search"),
	)
	res, err := Improve(context.Background(), Config{MaxExperiments: 6, MinGain: 0.05, Surface: s, LogPath: filepath.Join(dir, "experiments.log")}, env)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		kept   bool
		reason string
	}{
		{true, "kept: 0.20 → 0.60"},
		{false, "rejected (surface)"},
		{false, "not an allowed tool"},
		{false, "regression"},
		{false, "no gain"},
		{false, "no gain"}, // 0.62 vs 0.60 is under the minimum gain: noise must not win
	}
	if len(res.Records) != len(want) {
		t.Fatalf("%d records, want %d", len(res.Records), len(want))
	}
	for i, w := range want {
		r := res.Records[i]
		if r.Kept != w.kept || !strings.Contains(r.Reason, w.reason) {
			t.Errorf("exp %d: kept=%v reason=%q, want kept=%v containing %q", r.N, r.Kept, r.Reason, w.kept, w.reason)
		}
	}
	if res.Best.Title != "win" {
		t.Errorf("best = %q", res.Best.Title)
	}
	// Every experiment is in the log — the discarded ones most of all.
	b, _ := os.ReadFile(filepath.Join(dir, "experiments.log"))
	if n := strings.Count(string(b), "\n"); n != 6 {
		t.Errorf("log has %d lines, want 6 (one per experiment, kept or not)", n)
	}
	// strategy.md holds the kept strategy, not the last one tried.
	if got, _ := s.Read(); !strings.Contains(got, "# win") {
		t.Errorf("strategy.md = %q, want the kept strategy", got)
	}
}

func TestImproveStopsAtTheBudget(t *testing.T) {
	s, dir := newSurface(t)
	calls := 0
	env := Env{
		Measure: func(context.Context, Strategy) (Measurement, error) { return Measurement{Found: 1, Total: 10}, nil },
		Propose: func(context.Context, Strategy, Measurement) (string, error) {
			calls++
			return proposal("x", "kb_search"), nil
		},
	}
	if _, err := Improve(context.Background(), Config{MaxExperiments: 3, Surface: s, LogPath: filepath.Join(dir, "experiments.log")}, env); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("proposer called %d times, want exactly the budget of 3", calls)
	}
}

func TestCLIOfflineFindsTheLeverAndTheBrakesFire(t *testing.T) {
	var out bytes.Buffer
	dir := t.TempDir()
	code := runCLI([]string{"-offline", "-dir", dir}, &out)
	report := out.String()
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, report)
	}
	for _, want := range []string{
		"baseline  score 0.00  (0 of 9 facts, 0 documents opened)",
		"exp 1     kept: 0.00 → 1.00",
		"exp 2     rejected (surface)",
		"exp 3     rejected: invalid strategy: \"shell\" is not an allowed tool",
		"exp 4     discarded: regression",
		"exp 5     discarded: no gain",
		"improved: 0.00 → 1.00 (9 of 9 facts, 3 documents opened)",
		"surface:  clean",
		"scripted-proposer (NOT an LLM)",
		"no usage reported",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q\n%s", want, report)
		}
	}
	// What the run left on disk is the whole audit.
	if b, _ := os.ReadFile(filepath.Join(dir, "strategy.md")); !strings.Contains(string(b), "kb_read") {
		t.Errorf("strategy.md = %q, want the kept strategy", b)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.md")); err == nil {
		t.Error("the escape attempt wrote a file outside the working directory")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "experiments.log")); strings.Count(string(b), "\n") != 5 {
		t.Errorf("experiments.log = %q, want 5 lines", b)
	}
}

func TestImproveWithNothingBetterKeepsTheBaselineAndNamesEveryCause(t *testing.T) {
	s, dir := newSurface(t)
	env := fakeEnv(map[string]float64{"Baseline": 0.5, "w": 0.1}, proposal("w", "kb_search"))
	res, err := Improve(context.Background(), Config{MaxExperiments: 2, MinGain: 0.05, Surface: s, LogPath: filepath.Join(dir, "experiments.log")}, env)
	if err != nil || res.BestM.Score() != res.Baseline.Score() {
		t.Fatalf("nothing should have been kept: %+v, %v", res, err)
	}
	for _, r := range res.Records {
		if r.Kept || r.Reason == "" {
			t.Errorf("exp %d: a discard must name its cause: %+v", r.N, r)
		}
	}
}

func TestCLIRejectsBadFlags(t *testing.T) {
	for _, args := range [][]string{{"-experiments", "0"}, {"-nope"}} {
		var out bytes.Buffer
		if code := runCLI(args, &out); code != 2 || out.Len() != 0 {
			t.Errorf("%v: want exit 2 and empty stdout, got %d %q", args, code, out.String())
		}
	}
}
