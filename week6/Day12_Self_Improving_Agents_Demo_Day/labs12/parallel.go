package main

// ++ Advanced: parallel self-improvement workers.
//
// One round = K proposals scored concurrently (errgroup), each in its own
// isolated git worktree: a worker writes its candidate strategy.md into its
// own checkout and reads it back from there through the sandbox, so workers
// cannot clobber each other or the real strategy.md. The best score wins; it
// is kept only if it beats the current metric by more than ε, like a
// sequential step. Every candidate counts against the same budget and is
// audited — discards first, the winner last, so the metric chain replays.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/sandbox"
)

// isolator gives every worker its own root to write a candidate into.
type isolator interface {
	Create(worker int) (root string, cleanup func(), err error)
	Kind() string
}

// gitWorktrees: `git worktree add --detach` per worker; the strategy root is
// the same relative directory inside the checkout.
type gitWorktrees struct{ repo, rel, base string }

func (g gitWorktrees) Kind() string { return "git worktree" }

func (g gitWorktrees) Create(worker int) (string, func(), error) {
	dir := filepath.Join(g.base, fmt.Sprintf("w%d", worker))
	if out, err := exec.Command("git", "-C", g.repo, "worktree", "add", "--detach", dir, "HEAD").CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("git worktree add: %v: %s", err, out)
	}
	cleanup := func() {
		_ = exec.Command("git", "-C", g.repo, "worktree", "remove", "--force", dir).Run()
		_ = exec.Command("git", "-C", g.repo, "worktree", "prune").Run()
	}
	root := filepath.Join(dir, g.rel)
	if err := os.MkdirAll(root, 0o755); err != nil {
		cleanup()
		return "", nil, err
	}
	return root, cleanup, nil
}

// tempDirs is the fallback outside a git checkout (and in tests).
type tempDirs struct{ base string }

func (t tempDirs) Kind() string { return "temp dir" }

func (t tempDirs) Create(worker int) (string, func(), error) {
	dir, err := os.MkdirTemp(t.base, fmt.Sprintf("w%d-", worker))
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// newIsolator prefers git worktrees when root is inside a repository.
func newIsolator(root string) (isolator, func(), error) {
	base, err := os.MkdirTemp("", "selfimprove-workers-")
	if err != nil {
		return nil, nil, err
	}
	done := func() { _ = os.RemoveAll(base) }
	abs, _ := filepath.Abs(root)
	top, err := exec.Command("git", "-C", abs, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return tempDirs{base}, done, nil
	}
	repo := strings.TrimSpace(string(top))
	rel, err := filepath.Rel(filepath.FromSlash(repo), abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return tempDirs{base}, done, nil
	}
	return gitWorktrees{repo: repo, rel: rel, base: base}, done, nil
}

var workerFocus = []string{
	"search breadth (top_k or max_subqueries)",
	"what is read (read_docs or the sources list)",
	"the critics order or the drafting instruction",
	"cost: anything that makes the run faster without losing facts",
}

// proposerSlot is one worker's own proposer agent and sink.
type proposerSlot struct {
	agent agent.Agent
	sink  *ProposalSink
}

// runParallel runs rounds of k concurrent candidates until the budget stops it.
// Live, the k proposals are generated concurrently too; offline they are
// generated in order, so the scripted fake model stays deterministic.
func runParallel(ctx context.Context, eng *Engine, models Models, k int, onEntry func(Entry), out io.Writer) (Report, error) {
	var round int
	slots := make([]proposerSlot, k)
	for i := range slots {
		focus := workerFocus[i%len(workerFocus)]
		sink := NewProposalSink(eng.Root)
		a, err := NewProposerAgent(models.Proposer, sink, func() string {
			return eng.Brief() + fmt.Sprintf("\nParallel round %d: several variants are scored at once; this variant must focus on %s.", round, focus)
		})
		if err != nil {
			return Report{}, err
		}
		slots[i] = proposerSlot{agent: a, sink: sink}
	}
	eng.Proposer = slots[0].sink
	iso, done, err := newIsolator(eng.Root)
	if err != nil {
		return Report{}, err
	}
	defer done()
	if err := eng.Start(ctx); err != nil {
		return Report{}, err
	}
	rep := Report{Baseline: eng.CurrentScore()}
	mode := "по черзі"
	if models.Live {
		mode = "паралельно"
	}
	fmt.Fprintf(out, "базова метрика (dev): %.3f · паралельно %d кандидатів/раунд · ізоляція: %s · пропозиції: %s\n",
		rep.Baseline, k, iso.Kind(), mode)
	for round = 1; ; round++ {
		if why := eng.Exhausted(); why != "" {
			rep.Stop = why
			break
		}
		entries, err := eng.parallelRound(ctx, round, slots, iso, models.Live)
		if err != nil {
			return rep, err
		}
		for _, e := range entries {
			rep.add(e)
			onEntry(e)
		}
		if len(entries) == 0 {
			rep.Stop = eng.Exhausted()
			break
		}
	}
	rep.Best, rep.Tokens = eng.CurrentScore(), eng.Tokens()
	return rep, nil
}

type candidate struct {
	entry Entry
	strat Strategy
	m     Measurement
	err   error
	start time.Time
}

// parallelRound proposes up to len(slots) candidates, scores the valid ones
// concurrently in isolated roots, and keeps at most one.
func (e *Engine) parallelRound(ctx context.Context, round int, slots []proposerSlot, iso isolator, concurrent bool) ([]Entry, error) {
	var cands []*candidate
	for w := 1; w <= len(slots); w++ {
		if why := e.Exhausted(); why != "" {
			break
		}
		e.acct.Begin()
		c := &candidate{start: e.Now()}
		c.entry = e.newEntry(c.start)
		c.entry.Attempt, c.entry.Worker = round, w
		cands = append(cands, c)
	}
	propose := func(c *candidate) error {
		slot := slots[c.entry.Worker-1]
		if _, err := labrun.Run(ctx, slot.agent, "propose one variant"); err != nil {
			return fmt.Errorf("пропонент %d: %w", c.entry.Worker, err)
		}
		prop, err := slot.sink.Propose(ctx, Strategy{}, nil)
		c.entry.ProposerTokens = prop.Tokens
		if err != nil {
			c.entry.Verdict, c.entry.Reason = VerdictProposerFailed, err.Error()
			return nil
		}
		c.entry.TargetPath, c.entry.DiffSummary = prop.Path, prop.Summary
		c.entry.NewStrategy, c.entry.NewStrategySHA = prop.Content, contentSHA(prop.Content)
		if strat, reason := e.check(prop); reason != "" {
			c.entry.Verdict, c.entry.Reason = VerdictRefused, reason
		} else {
			c.strat = strat
		}
		return nil
	}
	pg := new(errgroup.Group)
	if !concurrent {
		pg.SetLimit(1)
	}
	for _, c := range cands {
		pg.Go(func() error { return propose(c) })
	}
	if err := pg.Wait(); err != nil {
		return nil, err
	}

	g, gctx := errgroup.WithContext(ctx)
	for _, c := range cands {
		if c.entry.Verdict != "" {
			continue
		}
		g.Go(func() error {
			root, cleanup, err := iso.Create(c.entry.Worker)
			if err != nil {
				c.err = err
				return nil
			}
			defer cleanup()
			if err := sandbox.WriteFile(root, sandbox.StrategyFile, []byte(c.entry.NewStrategy)); err != nil {
				c.err = err
				return nil
			}
			raw, err := sandbox.ReadFile(root, sandbox.StrategyFile)
			if err != nil {
				c.err = err
				return nil
			}
			s, err := ParseStrategy(string(raw))
			if err != nil {
				c.err = err
				return nil
			}
			c.m, c.err = e.Scorer.Score(gctx, s)
			return nil
		})
	}
	_ = g.Wait()

	var best *candidate
	for _, c := range cands {
		if c.entry.Verdict != "" {
			continue
		}
		c.entry.EvalTokens = c.m.Tokens
		if c.err != nil {
			c.entry.Verdict, c.entry.Reason = VerdictDiscard, "оцінку не завершено: "+c.err.Error()
			continue
		}
		c.entry.Scored, c.entry.NewMetric, c.entry.Detail = true, c.m.Score, c.m.Detail
		if best == nil || c.m.Score > best.m.Score {
			best = c
		}
	}
	if best != nil {
		best.entry.Verdict, best.entry.Reason = Decide(e.current, best.m.Score, e.Epsilon)
	}
	for _, c := range cands {
		if c.entry.Verdict == "" {
			c.entry.Verdict, c.entry.Reason = VerdictDiscard, fmt.Sprintf("програв у раунді %d: %.4f < %.4f (воркер %d)",
				round, c.m.Score, best.m.Score, best.entry.Worker)
		}
	}

	var out []Entry
	order := append([]*candidate{}, cands...)
	if best != nil && best.entry.Verdict == VerdictKeep {
		order = append(without(order, best), best) // the winner last: the chain replays
	}
	for _, c := range order {
		if c == best && c.entry.Verdict == VerdictKeep {
			if err := sandbox.WriteFile(e.Root, sandbox.StrategyFile, []byte(c.entry.NewStrategy)); err != nil {
				return out, fmt.Errorf("engine: застосування переможця: %w", err)
			}
			e.mu.Lock()
			e.current, e.doc, e.detail = c.m.Score, c.strat, c.m.Detail
			e.mu.Unlock()
		} else if c.entry.Verdict == VerdictDiscard {
			if err := e.revert(&c.entry); err != nil {
				return out, err
			}
		}
		saved, _, err := e.finish(c.entry, c.start)
		if err != nil {
			return out, err
		}
		out = append(out, saved)
	}
	return out, nil
}

func without(list []*candidate, x *candidate) []*candidate {
	out := list[:0:0]
	for _, c := range list {
		if c != x {
			out = append(out, c)
		}
	}
	return out
}
