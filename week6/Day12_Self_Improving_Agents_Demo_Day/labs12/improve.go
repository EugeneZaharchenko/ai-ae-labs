package main

// The self-improvement control loop (adapted from solution/improve.go):
// propose → check brakes → apply → score → keep or revert → audit.
//
// No ADK import here on purpose: this is loop engineering, a control system,
// not a framework feature. It is tested with plain Go fakes.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/sandbox"
)

// Proposal is what the agent asks to change in one experiment. Path comes
// from the agent on purpose: if the loop filled it in, brake (a) would test
// our own good manners, not a defence.
type Proposal struct {
	Path, Content, Summary string
	Tokens                 int // proposer cost, returned even with an error
}

// Proposer generates a candidate strategy.
type Proposer interface {
	Propose(ctx context.Context, current Strategy, history []Entry) (Proposal, error)
}

// Measurement is one scoring of a strategy.
type Measurement struct {
	Score  float64 // the ONE metric, higher is better
	Tokens int
	Detail string // human-readable; keep/discard never reads it
}

// Scorer turns a strategy into one comparable number.
type Scorer interface {
	Score(ctx context.Context, s Strategy) (Measurement, error)
}

// Guard is a deterministic check of a candidate before it is scored.
type Guard func(Strategy) error

// Budget is brake (b): a fixed, cheap evaluation cycle.
type Budget struct {
	MaxExperiments int           // hard cap on iterations
	PerExperiment  time.Duration // time for one iteration
	Total          time.Duration // 0 = bounded by count only
	MaxTokens      int           // 0 = no token cap
}

// Validate rejects a budget that bounds nothing. MaxExperiments = 0 is an
// error, not "unlimited" (in loopagent 0 means "forever").
func (b Budget) Validate() error {
	switch {
	case b.MaxExperiments < 1:
		return errors.New("budget: MaxExperiments має бути ≥ 1 (0 — не «безмежно», а помилка)")
	case b.PerExperiment <= 0:
		return errors.New("budget: PerExperiment має бути > 0")
	case b.Total < 0 || b.MaxTokens < 0:
		return errors.New("budget: від'ємні межі")
	}
	return nil
}

// StopReason says why the loop stopped.
type StopReason string

const (
	StopMaxExperiments      StopReason = "max_experiments_reached"
	StopTimeBudget          StopReason = "time_budget_exhausted"
	StopTokenBudget         StopReason = "token_budget_exhausted"
	StopProposerFailed      StopReason = "proposer_failed"
	StopStrategyMaxAttempts StopReason = "strategy_max_attempts_reached"
)

// Accountant counts the spent budget.
type Accountant struct {
	budget                  Budget
	used, tokens, projected int
	spent                   time.Duration
}

// Peek says whether Begin would refuse now.
func (a *Accountant) Peek() StopReason {
	switch {
	case a.used >= a.budget.MaxExperiments:
		return StopMaxExperiments
	case a.budget.Total > 0 && a.spent+a.budget.PerExperiment > a.budget.Total:
		return StopTimeBudget
	case a.budget.MaxTokens > 0 && a.tokens+a.projected > a.budget.MaxTokens:
		return StopTokenBudget
	}
	return ""
}

// Begin reserves the next experiment, or says why not.
func (a *Accountant) Begin() (StopReason, bool) {
	if why := a.Peek(); why != "" {
		return why, false
	}
	a.used++
	return "", true
}

// Charge books time and tokens; a full experiment's cost becomes the
// projection for the next one (the token cap refuses an experiment that would
// overshoot, it does not stop one in the middle).
func (a *Accountant) Charge(d time.Duration, tokens int, experiment bool) {
	a.spent += max(d, 0)
	a.tokens += max(tokens, 0)
	if experiment && tokens > 0 {
		a.projected = tokens
	}
}

// Decide is the fitness comparator. A tie is a discard: keep needs evidence,
// otherwise the strategy drifts by random walk.
func Decide(prev, cand, eps float64) (ExpVerdict, string) {
	delta := cand - prev
	switch {
	case delta > eps:
		return VerdictKeep, fmt.Sprintf("метрика зросла %.4f → %.4f (+%.4f > ε=%.4f)", prev, cand, delta, eps)
	case delta < -eps:
		return VerdictDiscard, fmt.Sprintf("регресія %.4f → %.4f (%.4f)", prev, cand, delta)
	}
	return VerdictDiscard, fmt.Sprintf("нічия %.4f → %.4f (|%.4f| ≤ ε=%.4f): без доказу покращення зміну відкочено", prev, cand, delta, eps)
}

// Engine is the control system. Sequential by definition: each experiment is
// compared with the state the previous one left.
type Engine struct {
	Root     string // directory with strategy.md and experiments/
	Budget   Budget
	Epsilon  float64
	Guards   []Guard
	Proposer Proposer
	Scorer   Scorer
	Audit    *AuditLog
	GitSHA   string
	Now      func() time.Time

	acct    *Accountant
	doc     Strategy
	current float64
	detail  string
	history []Entry
	started bool
	mu      sync.Mutex // guards Brief, read by the proposer goroutine
}

// Report sums up a run.
type Report struct {
	Baseline, Best           float64
	Tokens                   int
	Entries                  []Entry
	Keeps, Discards, Refused int
	Stop                     StopReason
}

// Start validates the setup, reads the strategy and measures the baseline.
// The baseline is a reference point, not an experiment: it is charged to the
// token budget but not counted as an iteration.
func (e *Engine) Start(ctx context.Context) error {
	if e.started {
		return nil
	}
	switch {
	case e.Root == "" || e.Proposer == nil || e.Scorer == nil:
		return errors.New("engine: потрібні Root, Proposer і Scorer")
	case e.Audit == nil:
		return errors.New("engine: цикл без аудиту заборонений")
	case e.Epsilon < 0:
		return errors.New("engine: Epsilon < 0")
	}
	if err := e.Budget.Validate(); err != nil {
		return err
	}
	if e.Now == nil {
		e.Now = time.Now
	}
	raw, err := sandbox.ReadFile(e.Root, sandbox.StrategyFile)
	if err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	doc, err := ParseStrategy(string(raw))
	if err != nil {
		return fmt.Errorf("engine: базова стратегія недійсна: %w", err)
	}
	m, err := e.Scorer.Score(ctx, doc)
	e.acct = &Accountant{budget: e.Budget}
	e.acct.Charge(0, m.Tokens, false)
	if err != nil {
		return fmt.Errorf("engine: базовий вимір метрики: %w", err)
	}
	e.acct.projected = m.Tokens
	e.mu.Lock()
	e.doc, e.current, e.detail = doc, m.Score, m.Detail
	e.mu.Unlock()
	e.started = true
	return nil
}

// Exhausted reports whether the next Step would stop at once.
func (e *Engine) Exhausted() StopReason {
	if !e.started {
		return ""
	}
	if e.acct.used >= e.doc.MaxAttempts {
		return StopStrategyMaxAttempts
	}
	return e.acct.Peek()
}

// Step runs exactly one experiment. It returns an error only when the
// mechanism itself broke (audit or revert failed): going on would lose the trail.
func (e *Engine) Step(ctx context.Context) (Entry, StopReason, error) {
	if !e.started {
		return Entry{}, "", errors.New("engine: викличте Start перед Step")
	}
	if e.acct.used >= e.doc.MaxAttempts {
		return Entry{}, StopStrategyMaxAttempts, nil
	}
	if why, ok := e.acct.Begin(); !ok {
		return Entry{}, why, nil
	}
	ctx, cancel := context.WithTimeout(ctx, e.Budget.PerExperiment)
	defer cancel()
	start := e.Now()
	entry := e.newEntry(start)

	prop, err := e.Proposer.Propose(ctx, e.doc, e.history)
	entry.ProposerTokens = prop.Tokens
	if err != nil {
		entry.Verdict, entry.Reason = VerdictProposerFailed, err.Error()
		if _, _, err := e.finish(entry, start); err != nil {
			return Entry{}, "", err
		}
		return Entry{}, StopProposerFailed, nil
	}
	entry.TargetPath, entry.DiffSummary, entry.NewStrategy = prop.Path, prop.Summary, prop.Content
	entry.NewStrategySHA = contentSHA(prop.Content)

	cand, reason := e.check(prop)
	if reason != "" {
		entry.Verdict, entry.Reason = VerdictRefused, reason
		return e.finish(entry, start)
	}
	if err := sandbox.WriteFile(e.Root, sandbox.StrategyFile, []byte(prop.Content)); err != nil {
		return Entry{}, "", fmt.Errorf("engine: застосування кандидата: %w", err)
	}
	m, scoreErr := e.Scorer.Score(ctx, cand)
	entry.EvalTokens = m.Tokens
	if scoreErr != nil {
		entry.Verdict, entry.Reason = VerdictDiscard, "оцінку не завершено: "+scoreErr.Error()
	} else {
		entry.Scored, entry.NewMetric, entry.Detail = true, m.Score, m.Detail
		entry.Verdict, entry.Reason = Decide(e.current, m.Score, e.Epsilon)
	}
	if entry.Verdict == VerdictKeep {
		e.mu.Lock()
		e.current, e.doc, e.detail = m.Score, cand, m.Detail
		e.mu.Unlock()
		return e.finish(entry, start)
	}
	if err := e.revert(&entry); err != nil {
		return Entry{}, "", err
	}
	return e.finish(entry, start)
}

func (e *Engine) newEntry(start time.Time) Entry {
	return Entry{
		Attempt: e.acct.used, TS: start, PrevMetric: e.current, GitSHA: e.GitSHA,
		OldStrategy: e.doc.Raw, OldStrategySHA: contentSHA(e.doc.Raw),
	}
}

// check applies brakes (a) and (d) and the guards; "" means the candidate may
// be scored. The loop re-checks the path even if the tool already did: brakes
// that trust their caller are not independent.
func (e *Engine) check(p Proposal) (Strategy, string) {
	if _, err := sandbox.Resolve(e.Root, p.Path); err != nil {
		return Strategy{}, err.Error()
	}
	if filepath.Clean(filepath.FromSlash(p.Path)) != sandbox.StrategyFile {
		return Strategy{}, fmt.Sprintf("%s: пропозиція мусить змінювати %s, а не %q", sandbox.ErrNotMutable, sandbox.StrategyFile, p.Path)
	}
	cand, err := ParseStrategy(p.Content)
	if err != nil {
		return Strategy{}, err.Error()
	}
	for _, g := range e.Guards {
		if err := g(cand); err != nil {
			return Strategy{}, err.Error()
		}
	}
	return cand, ""
}

// revert restores the kept strategy and stores the discarded candidate in
// experiments/ — the only other place the agent's output may live.
func (e *Engine) revert(entry *Entry) error {
	if err := sandbox.WriteFile(e.Root, sandbox.StrategyFile, []byte(e.doc.Raw)); err != nil {
		return fmt.Errorf("engine: відкат: %w", err)
	}
	name := fmt.Sprintf("%s/exp-%s-attempt%02d.md", sandbox.ExperimentsDir, e.Now().Format("20060102-150405"), entry.Attempt)
	if entry.Worker > 0 {
		name = strings.TrimSuffix(name, ".md") + fmt.Sprintf("-w%d.md", entry.Worker)
	}
	if err := sandbox.WriteFile(e.Root, name, []byte(entry.NewStrategy)); err != nil {
		return fmt.Errorf("engine: збереження дискарду: %w", err)
	}
	entry.DiscardPath = name
	return nil
}

// finish books the cost and appends the entry: EVERY experiment passes here.
func (e *Engine) finish(entry Entry, start time.Time) (Entry, StopReason, error) {
	elapsed := e.Now().Sub(start)
	e.acct.Charge(elapsed, entry.EvalTokens+entry.ProposerTokens, true)
	entry.DurationSec = elapsed.Seconds()
	if !entry.Scored {
		entry.NewMetric = entry.PrevMetric
	}
	saved, err := e.Audit.Append(entry)
	if err != nil {
		return Entry{}, "", err
	}
	e.history = append(e.history, saved)
	return saved, "", nil
}

// Run loops until the budget stops it.
func (e *Engine) Run(ctx context.Context) (Report, error) {
	if err := e.Start(ctx); err != nil {
		return Report{}, err
	}
	rep := Report{Baseline: e.current}
	for {
		entry, why, err := e.Step(ctx)
		if err != nil {
			return rep, err
		}
		if why != "" {
			rep.Stop = why
			break
		}
		rep.add(entry)
	}
	rep.Best, rep.Tokens = e.current, e.acct.tokens
	return rep, nil
}

func (r *Report) add(entry Entry) {
	r.Entries = append(r.Entries, entry)
	switch entry.Verdict {
	case VerdictKeep:
		r.Keeps++
	case VerdictDiscard:
		r.Discards++
	case VerdictRefused:
		r.Refused++
	}
}

// Strategy is the current kept strategy.
func (e *Engine) Strategy() Strategy {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.doc
}

// CurrentScore is the metric of the kept strategy.
func (e *Engine) CurrentScore() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.current
}

// Tokens spent so far.
func (e *Engine) Tokens() int {
	if e.acct == nil {
		return 0
	}
	return e.acct.tokens
}

// Brief is what the proposer sees: the current strategy, how it behaved, and
// what was already tried. Never the expected facts.
func (e *Engine) Brief() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "Current strategy (score %.3f):\n<<<STRATEGY\n%s\n>>>\n\n", e.current, strings.TrimSpace(e.doc.Raw))
	fmt.Fprintf(&b, "How it behaved on the dev questions:\n%s\n\n", e.detail)
	fmt.Fprintf(&b, "Experiments so far: %d\n", len(e.history))
	for _, h := range e.history[max(0, len(e.history)-8):] {
		fmt.Fprintf(&b, "- #%d %s: %s (%s)\n", h.ID, h.Verdict, h.DiffSummary, h.Reason)
	}
	return b.String()
}
