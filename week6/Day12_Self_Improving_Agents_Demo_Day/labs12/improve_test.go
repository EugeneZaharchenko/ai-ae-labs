package main

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/metric"
	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/sandbox"
)

// scriptProposer returns the queued proposals, then fails.
type scriptProposer struct{ props []Proposal }

func (p *scriptProposer) Propose(context.Context, Strategy, []Entry) (Proposal, error) {
	if len(p.props) == 0 {
		return Proposal{Tokens: 5}, errors.New("script exhausted")
	}
	x := p.props[0]
	p.props = p.props[1:]
	return x, nil
}

// topKScorer: the score is top_k/10, so a candidate's effect is predictable.
type topKScorer struct {
	calls int
	fail  error
}

func (s *topKScorer) Score(_ context.Context, st Strategy) (Measurement, error) {
	s.calls++
	return Measurement{Score: float64(st.TopK) / 10, Tokens: 100, Detail: "fixture"}, s.fail
}

func candidateWithTopK(k int) Proposal {
	s := strategyWith(func(s *Strategy) { s.TopK = k })
	return Proposal{Path: sandbox.StrategyFile, Content: s.Render(), Summary: "top_k=" + string(rune('0'+k))}
}

func newTestEngine(t *testing.T, budget Budget, props ...Proposal) (*Engine, *bytes.Buffer, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, sandbox.StrategyFile), []byte(strategyWith(nil).Render()), 0o600); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	now := time.Unix(0, 0)
	return &Engine{
		Root: root, Budget: budget, Epsilon: 0.04,
		Proposer: &scriptProposer{props: props}, Scorer: &topKScorer{},
		Audit: NewAuditLog(&log, 0), GitSHA: "abc123",
		Now: func() time.Time { now = now.Add(time.Second); return now },
	}, &log, root
}

func TestBudgetStopsExactlyAtLimit(t *testing.T) {
	t.Parallel()
	props := []Proposal{candidateWithTopK(2), candidateWithTopK(3), candidateWithTopK(4), candidateWithTopK(5)}
	eng, log, _ := newTestEngine(t, Budget{MaxExperiments: 3, PerExperiment: time.Minute}, props...)
	rep, err := eng.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Entries) != 3 || rep.Stop != StopMaxExperiments {
		t.Fatalf("entries=%d stop=%s; must stop exactly at 3", len(rep.Entries), rep.Stop)
	}
	if strings.Count(log.String(), "\n") != 3 || rep.Best != 0.4 {
		t.Fatalf("log lines / best wrong: %d, %.2f", strings.Count(log.String(), "\n"), rep.Best)
	}
	// The baseline is a reference point, charged but not counted as an experiment.
	if rep.Tokens != 100+3*100 {
		t.Fatalf("tokens = %d", rep.Tokens)
	}
}

func TestStrategyMaxAttemptsIsItsOwnCap(t *testing.T) {
	t.Parallel()
	capped := func(k int) Proposal {
		s := strategyWith(func(s *Strategy) { s.TopK, s.MaxAttempts = k, 2 })
		return Proposal{Path: sandbox.StrategyFile, Content: s.Render(), Summary: "capped"}
	}
	eng, _, root := newTestEngine(t, Budget{MaxExperiments: 10, PerExperiment: time.Minute}, capped(2), capped(3), capped(4))
	if err := os.WriteFile(filepath.Join(root, sandbox.StrategyFile), []byte(capped(1).Content), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := eng.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Stop != StopStrategyMaxAttempts || len(rep.Entries) != 2 {
		t.Fatalf("stop=%s entries=%d; the strategy's own max_attempts=2 must hold under a budget of 10", rep.Stop, len(rep.Entries))
	}
}

func TestTokenBudgetProjection(t *testing.T) {
	t.Parallel()
	eng, _, _ := newTestEngine(t, Budget{MaxExperiments: 10, PerExperiment: time.Minute, MaxTokens: 250},
		candidateWithTopK(2), candidateWithTopK(3), candidateWithTopK(4))
	rep, err := eng.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// baseline 100 + 1 experiment 100 = 200; the next would need ~100 more → stop.
	if rep.Stop != StopTokenBudget || len(rep.Entries) != 1 {
		t.Fatalf("stop=%s entries=%d", rep.Stop, len(rep.Entries))
	}
}

func TestBudgetValidate(t *testing.T) {
	t.Parallel()
	for _, b := range []Budget{{}, {MaxExperiments: 1}, {MaxExperiments: 1, PerExperiment: 1, MaxTokens: -1}} {
		if b.Validate() == nil {
			t.Errorf("Validate(%+v) = nil", b)
		}
	}
	a, err := NewProposerAgent(fakellm.New("p"), NewProposalSink(t.TempDir()), func() string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSelfImproveAgent(&Engine{}, a, 0, nil); err == nil {
		t.Fatal("loopagent MaxIterations=0 means forever and must be rejected")
	}
}

func TestKeepDiscardRefuseRevert(t *testing.T) {
	t.Parallel()
	twoMetrics := strings.Replace(strategyWith(nil).Render(), "- facts_found_ratio", "- facts_found_ratio\n- readability", 1)
	props := []Proposal{
		candidateWithTopK(3), // keep 0.1 → 0.3
		candidateWithTopK(1), // regression → discard + revert
		{Path: "../main.go", Content: candidateWithTopK(5).Content},         // brake (a)
		{Path: "eval/dev.json", Content: candidateWithTopK(5).Content},      // brake (a)
		{Path: "experiments/x.md", Content: candidateWithTopK(5).Content},   // mutable, but not the strategy
		{Path: sandbox.StrategyFile, Content: twoMetrics},                   // brake (c)/(d): parser
		{Path: sandbox.StrategyFile, Content: candidateWithTopK(3).Content}, // tie → discard
	}
	eng, log, root := newTestEngine(t, Budget{MaxExperiments: 7, PerExperiment: time.Minute}, props...)
	rep, err := eng.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []ExpVerdict{VerdictKeep, VerdictDiscard, VerdictRefused, VerdictRefused, VerdictRefused, VerdictRefused, VerdictDiscard}
	for i, e := range rep.Entries {
		if e.Verdict != want[i] {
			t.Errorf("#%d verdict %s, want %s (%s)", i+1, e.Verdict, want[i], e.Reason)
		}
	}
	cur, _ := sandbox.ReadFile(root, sandbox.StrategyFile)
	if s, _ := ParseStrategy(string(cur)); s.TopK != 3 {
		t.Fatalf("after a discard the kept strategy must be restored; top_k=%d", s.TopK)
	}
	if _, err := os.Stat(filepath.Join(root, "main.go")); !os.IsNotExist(err) {
		t.Fatal("brake (a) failed: main.go written")
	}
	d := rep.Entries[1]
	if d.DiscardPath == "" || !strings.HasPrefix(filepath.ToSlash(d.DiscardPath), "experiments/") {
		t.Fatalf("discard not stored in experiments/: %q", d.DiscardPath)
	}
	if _, err := os.Stat(filepath.Join(root, d.DiscardPath)); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"prev_metric"`, `"new_metric"`, `"verdict"`, `"diff_summary"`, `"old_strategy"`,
		`"new_strategy"`, `"git_sha":"abc123"`, `"duration_sec"`} {
		if !strings.Contains(log.String(), field) {
			t.Errorf("experiments.log lacks %s", field)
		}
	}
}

func TestScoreFailureIsDiscardAndProposerFailureStops(t *testing.T) {
	t.Parallel()
	eng, _, _ := newTestEngine(t, Budget{MaxExperiments: 5, PerExperiment: time.Minute}, candidateWithTopK(5))
	sc := &topKScorer{}
	eng.Scorer = sc
	if err := eng.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	sc.fail = errors.New("network down")
	e, why, err := eng.Step(context.Background())
	if err != nil || why != "" || e.Verdict != VerdictDiscard || !strings.Contains(e.Reason, "оцінку не завершено") {
		t.Fatalf("a failed measurement must be a discard, never a keep: %+v %s %v", e, why, err)
	}
	if _, why, _ := eng.Step(context.Background()); why != StopProposerFailed {
		t.Fatalf("stop = %s", why)
	}
}

func TestGuardRefusesGoldenLeak(t *testing.T) {
	t.Parallel()
	cases := []metric.Case{{ID: "A", Facts: []metric.Fact{{Forms: []string{"compliance officer"}}}}}
	s := strategyWith(func(s *Strategy) { s.Instruction = "Always say the Compliance Officer approves." })
	s.Raw = s.Render()
	if err := NoGoldenLeak(cases)(s); err == nil {
		t.Fatal("cheat sheet accepted")
	}
	if err := NoGoldenLeak(cases)(strategyWith(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestEngineStartErrors(t *testing.T) {
	t.Parallel()
	eng, _, root := newTestEngine(t, Budget{MaxExperiments: 1, PerExperiment: time.Minute})
	if _, _, err := (&Engine{}).Step(context.Background()); err == nil {
		t.Fatal("Step before Start accepted")
	}
	if err := os.WriteFile(filepath.Join(root, sandbox.StrategyFile), []byte("# broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := eng.Start(context.Background()); err == nil {
		t.Fatal("a broken baseline strategy must stop the loop")
	}
	if err := (&Engine{Root: root}).Start(context.Background()); err == nil {
		t.Fatal("engine without audit accepted")
	}
}

func TestAuditReplay(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := NewAuditLog(&buf, 0)
	for _, e := range []Entry{
		{Attempt: 1, Verdict: VerdictKeep, PrevMetric: 0.1, NewMetric: 0.5},
		{Attempt: 2, Verdict: VerdictDiscard, PrevMetric: 0.5, NewMetric: 0.4},
		{Attempt: 3, Verdict: VerdictRefused, PrevMetric: 0.5, NewMetric: 0.5},
		{Attempt: 1, Verdict: VerdictKeep, PrevMetric: 0.6, NewMetric: 0.7}, // a new run measures its own baseline
	} {
		if _, err := log.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := ReadEntries(&buf)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Replay(entries)
	if err != nil || st.Keeps != 2 || st.Final != 0.7 {
		t.Fatalf("Replay = %+v, %v", st, err)
	}
	deleted := append(append([]Entry{}, entries[:1]...), entries[2:]...)
	if _, err := Replay(deleted); !errors.Is(err, ErrAuditBroken) {
		t.Fatal("deleted line not detected")
	}
	broken := append([]Entry{}, entries...)
	broken[2].PrevMetric = 0.9
	if _, err := Replay(broken); !errors.Is(err, ErrAuditBroken) {
		t.Fatal("broken metric chain not detected")
	}
	if _, err := Replay(nil); err == nil {
		t.Fatal("empty log accepted")
	}
	if _, err := ReadEntries(strings.NewReader("{bad")); err == nil {
		t.Fatal("bad line accepted")
	}
}

// The loopagent cap is a second, independent brake: budget 10, MaxIterations 2.
func TestLoopAgentMaxIterationsHolds(t *testing.T) {
	t.Parallel()
	eng, _, root := newTestEngine(t, Budget{MaxExperiments: 10, PerExperiment: time.Minute})
	sink := NewProposalSink(root)
	eng.Proposer = sink
	var turns []fakellm.Turn
	for k := 2; k <= 6; k++ {
		turns = append(turns, fakellm.CallTurn("write_strategy", map[string]any{
			"path": sandbox.StrategyFile, "content": candidateWithTopK(k).Content, "summary": "more"}),
			fakellm.TextTurn("ok"))
	}
	proposer, err := NewProposerAgent(fakellm.New("p", turns...), sink, eng.Brief)
	if err != nil {
		t.Fatal(err)
	}
	var got []Entry
	loop, err := NewSelfImproveAgent(eng, proposer, 2, func(e Entry) { got = append(got, e) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runAgent(context.Background(), loop, "go"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("loopagent ran %d experiments, MaxIterations=2", len(got))
	}
}

// The write_strategy tool refuses a path outside the surface; the model sees
// the refusal and nothing is queued.
func TestWriteStrategyToolRefuses(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sink := NewProposalSink(root)
	model := fakellm.New("p",
		fakellm.CallTurn("write_strategy", map[string]any{"path": "../main.go", "content": "x", "summary": "escape"}),
		fakellm.CallTurn("write_strategy", map[string]any{"path": "experiments/a.md", "content": "x", "summary": "wrong file"}),
		fakellm.TextTurn("gave up"))
	a, err := NewProposerAgent(model, sink, func() string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runAgent(context.Background(), a, "go"); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Propose(context.Background(), Strategy{}, nil); !errors.Is(err, ErrNoProposal) {
		t.Fatalf("refused writes must not be queued: %v", err)
	}
}

// slowModel blocks until the context ends: a case that runs out of time.
type slowModel struct{}

func (slowModel) Name() string { return "slow" }
func (slowModel) GenerateContent(ctx context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		<-ctx.Done()
		yield(nil, ctx.Err())
	}
}

func TestEvalCaseTimeoutCountsAsZero(t *testing.T) {
	t.Parallel()
	if EvalCaseTimeout != 60*time.Second {
		t.Fatalf("eval node timeout = %s, the spec fixes 60s", EvalCaseTimeout)
	}
	cases := []metric.Case{{ID: "T1", Question: "Who approves a refund?", Facts: []metric.Fact{{Type: metric.TypeName, Forms: []string{"compliance officer"}}}}}
	sc := &PipelineScorer{Lib: testLib, Writer: slowModel{}, Critic: offlineCritic{}, Cases: cases, Parallel: 1, Timeout: 200 * time.Millisecond}
	m, err := sc.Score(context.Background(), strategyWith(nil))
	if err != nil || m.Score != 0 || !strings.Contains(m.Detail, "TIMED OUT") {
		t.Fatalf("Score = %+v, %v; a slow case is 0 facts, not a broken measurement", m, err)
	}
	fast := &PipelineScorer{Lib: testLib, Writer: offlineWriter{}, Critic: offlineCritic{}, Cases: cases, Parallel: 1}
	if m, err := fast.Score(context.Background(), strategyWith(func(s *Strategy) { s.TopK = 3 })); err != nil || m.Score != 1 {
		t.Fatalf("fast Score = %+v, %v", m, err)
	}
}
