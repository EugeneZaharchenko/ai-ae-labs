package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/sandbox"
)

func proposalTurns(ks ...int) []fakellm.Turn {
	var turns []fakellm.Turn
	for _, k := range ks {
		turns = append(turns,
			fakellm.CallTurn("write_strategy", map[string]any{"path": sandbox.StrategyFile, "content": candidateWithTopK(k).Content, "summary": "variant"}),
			fakellm.TextTurn("ok"))
	}
	return turns
}

// One parallel round: three candidates scored concurrently in isolated roots,
// the best kept, the others discarded and stored, the winner logged last.
func TestParallelRoundKeepsBest(t *testing.T) {
	t.Parallel()
	eng, log, root := newTestEngine(t, Budget{MaxExperiments: 3, PerExperiment: time.Minute})
	models := Models{Proposer: fakellm.New("p", proposalTurns(3, 6, 2)...)}
	var got []Entry
	var out bytes.Buffer
	rep, err := runParallel(context.Background(), eng, models, 3, func(e Entry) { got = append(got, e) }, &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || rep.Keeps != 1 || rep.Discards != 2 {
		t.Fatalf("entries=%d keeps=%d discards=%d\n%s", len(got), rep.Keeps, rep.Discards, out.String())
	}
	last := got[len(got)-1]
	if last.Verdict != VerdictKeep || last.Worker != 2 || last.NewMetric != 0.6 {
		t.Fatalf("winner must be worker 2 (top_k=6) and logged last: %+v", last)
	}
	cur, _ := sandbox.ReadFile(root, sandbox.StrategyFile)
	if s, _ := ParseStrategy(string(cur)); s.TopK != 6 {
		t.Fatalf("strategy.md top_k=%d, want the winner's 6", s.TopK)
	}
	entries, err := ReadEntries(log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Replay(entries); err != nil {
		t.Fatalf("parallel log must replay: %v", err)
	}
	if rep.Stop != StopMaxExperiments {
		t.Fatalf("stop = %s", rep.Stop)
	}
}

func TestIsolators(t *testing.T) {
	t.Parallel()
	td := tempDirs{base: t.TempDir()}
	root, cleanup, err := td.Create(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.WriteFile(root, sandbox.StrategyFile, []byte("x")); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("temp dir not removed")
	}

	iso, done, err := newIsolator(t.TempDir()) // not a repo → temp dirs
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if iso.Kind() != "temp dir" {
		t.Fatalf("outside git: %s", iso.Kind())
	}
}

func TestGitWorktreeIsolator(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a real git worktree")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	iso, done, err := newIsolator(".")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if iso.Kind() != "git worktree" {
		t.Skipf("not inside a git checkout: %s", iso.Kind())
	}
	root, cleanup, err := iso.Create(1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(filepath.ToSlash(root), "labs12") {
		t.Fatalf("worktree root %s is not the lab directory", root)
	}
	if err := sandbox.WriteFile(root, sandbox.StrategyFile, []byte("candidate")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(sandbox.StrategyFile); string(b) == "candidate" {
		t.Fatal("the worker wrote into the real strategy.md")
	}
	cleanup()
}
