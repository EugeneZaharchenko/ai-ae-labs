// Тести гальм і розбору стратегії: кожне з чотирьох гальм перевіряється як
// окрема гарантія, а не як побічний ефект щасливого шляху.
package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/ledgerkb"
	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/offline"
)

var allTools = ledgerkb.AllToolNames()

func TestParseStrategy_RoundTrip(t *testing.T) {
	doc, err := ParseStrategy(RenderStrategy(baselineStrategy()), allTools)
	if err != nil {
		t.Fatalf("ParseStrategy: %v", err)
	}
	if doc.Metric != "facts_found_ratio" || doc.MaxAttempts != 6 || len(doc.Tools) != 1 {
		t.Errorf("розібрано неправильно: %+v", doc)
	}
}

func TestParseStrategy_Rejects(t *testing.T) {
	good := RenderStrategy(baselineStrategy())
	long := strings.Replace(good, "Search the knowledge base once", strings.Repeat("x", MaxInstructionRunes+1), 1)
	cases := map[string]struct {
		raw  string
		want error
	}{
		"дві метрики":       {strings.Replace(good, "facts_found_ratio\n", "facts_found_ratio\nlatency\n", 1), ErrMetricNotSingle},
		"нема метрики":      {strings.Replace(good, "## Metric\nfacts_found_ratio\n", "", 1), ErrMetricNotSingle},
		"чужий інструмент":  {strings.Replace(good, "- kb_search", "- shell_exec", 1), ErrToolNotAllowed},
		"дубль інструмента": {strings.Replace(good, "- kb_search\n", "- kb_search\n- kb_search\n", 1), ErrStrategyMalformed},
		"невідома секція":   {good + "\n## Secrets\nhi\n", ErrStrategyMalformed},
		"дубль секції":      {good + "\n## Metric\nx\n", ErrStrategyMalformed},
		"нема заголовка":    {strings.Replace(good, "# Baseline strategy: search once, answer from snippets\n", "", 1), ErrStrategyMalformed},
		"два заголовки":     {"# a\n" + good, ErrStrategyMalformed},
		"нема інструкції":   {strings.Replace(good, "Search the knowledge base once and answer in one sentence from the search results.", "", 1), ErrStrategyMalformed},
		"надто довга":       {long, ErrStrategyMalformed},
		"нема stop":         {strings.Replace(good, "## Stop conditions\n- max_attempts = 6\n", "", 1), ErrStrategyMalformed},
		"max_attempts 0":    {strings.Replace(good, "max_attempts = 6", "max_attempts = 0", 1), ErrStrategyMalformed},
		"max_attempts abc":  {strings.Replace(good, "max_attempts = 6", "max_attempts = many", 1), ErrStrategyMalformed},
		"нема tools":        {strings.Replace(good, "## Tools\n- kb_search\n", "## Tools\n", 1), ErrStrategyMalformed},
	}
	for name, c := range cases {
		if _, err := ParseStrategy(c.raw, allTools); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, очікували %v", name, err, c.want)
		}
	}
	// Порожній allowlist — fail-closed.
	if _, err := ParseStrategy(good, nil); !errors.Is(err, ErrToolNotAllowed) {
		t.Errorf("порожній allowlist мав закрити все: %v", err)
	}
}

func TestStripBulletAndAttempts(t *testing.T) {
	for in, want := range map[string]string{"- a": "a", "* b — x": "b", "1. c": "c", "  d  ": "d"} {
		if got := stripBullet(in); got != want {
			t.Errorf("stripBullet(%q) = %q, очікували %q", in, got, want)
		}
	}
	if n, ok := parseMaxAttempts([]string{"- max_attempts: 12 (hard)"}); !ok || n != 12 {
		t.Errorf("parseMaxAttempts = %d, %v", n, ok)
	}
	if _, ok := parseMaxAttempts([]string{"- nothing"}); ok {
		t.Error("очікували not found")
	}
}

func TestSurface_RejectsEscapes(t *testing.T) {
	root := t.TempDir()
	s, err := NewSurface(root, "strategy.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", "../x", "other.md", filepath.Join(root, "other.md"), "sub/../other.md", "/etc/passwd"} {
		if _, err := s.Resolve(p); !errors.Is(err, ErrOutsideSurface) {
			t.Errorf("Resolve(%q) = %v, очікували ErrOutsideSurface", p, err)
		}
	}
	for _, bad := range []string{"", "..", "../x", "/abs", "."} {
		if _, err := NewSurface(root, bad); err == nil {
			t.Errorf("NewSurface(rel=%q) мав відхилити", bad)
		}
	}
	if _, err := NewSurface("", "a"); err == nil {
		t.Error("порожній root мав відхилитись")
	}
	if _, err := NewSurface(filepath.Join(root, "nope"), "a"); err == nil {
		t.Error("неіснуючий root мав відхилитись")
	}
	// Симлінк на місці артефакту.
	target := filepath.Join(root, "real.md")
	_ = os.WriteFile(target, []byte("x"), 0o600)
	_ = os.Symlink(target, filepath.Join(root, "strategy.md"))
	if _, err := s.Resolve("strategy.md"); !errors.Is(err, ErrOutsideSurface) {
		t.Errorf("симлінк мав відхилитись: %v", err)
	}
	if s.Root() == "" || s.Path() == "" {
		t.Error("Root/Path порожні")
	}
}

func TestBudgetValidateAndAccountant(t *testing.T) {
	for name, b := range map[string]Budget{
		"0 експериментів": {PerExperiment: time.Second},
		"0 часу":          {MaxExperiments: 1},
		"Total<0":         {MaxExperiments: 1, PerExperiment: time.Second, Total: -1},
		"MaxTokens<0":     {MaxExperiments: 1, PerExperiment: time.Second, MaxTokens: -1},
	} {
		if b.Validate() == nil {
			t.Errorf("%s: Validate мав відхилити", name)
		}
	}
	a := NewAccountant(Budget{MaxExperiments: 5, PerExperiment: time.Second, Total: 2 * time.Second, MaxTokens: 100})
	a.Project(40)
	a.ChargeTokens(0, true)
	if _, ok := a.Begin(); !ok {
		t.Fatal("перший експеримент мав стартувати")
	}
	a.ChargeTokens(60, true)
	a.Charge(time.Second)
	a.Charge(-time.Second)
	if why, ok := a.Begin(); ok || why != StopTokenBudget {
		t.Errorf("60+проєкція 60 > 100 мало зупинити за токенами, є %q", why)
	}
	if a.Tokens() != 60 || a.Used() != 1 || a.Spent() != time.Second {
		t.Errorf("облік: tokens=%d used=%d spent=%v", a.Tokens(), a.Used(), a.Spent())
	}
	a.Charge(time.Second)
	if why := a.Peek(); why != StopTimeBudget && why != StopTokenBudget {
		t.Errorf("Peek = %q", why)
	}
}

// Токенова стеля зупиняє цикл ДО наступного експерименту, і причина лишається в Report.
func TestEngineStopsOnTokenBudget(t *testing.T) {
	surface := newTestSurface(t, baselineStrategy())
	var buf bytes.Buffer
	eng := &Engine{
		Surface: surface, Allowlist: allTools,
		Budget:   Budget{MaxExperiments: 10, PerExperiment: time.Second, MaxTokens: 250},
		Epsilon:  0.5,
		Proposer: tieProposer{rel: surface.Rel()},
		Scorer:   &FixtureScorer{Default: 1, Tokens: 100},
		Audit:    NewAuditLog(&buf),
	}
	rep, err := eng.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Stop != StopTokenBudget || rep.Experiments != 1 || rep.Discards != 1 {
		t.Errorf("report = %+v", rep)
	}
	if eng.Tokens() != 200 || eng.Exhausted() != StopTokenBudget {
		t.Errorf("tokens=%d exhausted=%q", eng.Tokens(), eng.Exhausted())
	}
}

func TestEngineStartValidation(t *testing.T) {
	surface := newTestSurface(t, baselineStrategy())
	ok := func() *Engine {
		return &Engine{Surface: surface, Allowlist: allTools,
			Budget: Budget{MaxExperiments: 1, PerExperiment: time.Second}, Proposer: tieProposer{rel: "strategy.md"},
			Scorer: &FixtureScorer{}, Audit: NewAuditLog(new(bytes.Buffer))}
	}
	for name, mut := range map[string]func(*Engine){
		"surface":  func(e *Engine) { e.Surface = nil },
		"proposer": func(e *Engine) { e.Proposer = nil },
		"scorer":   func(e *Engine) { e.Scorer = nil },
		"audit":    func(e *Engine) { e.Audit = nil },
		"epsilon":  func(e *Engine) { e.Epsilon = -1 },
		"budget":   func(e *Engine) { e.Budget = Budget{} },
	} {
		e := ok()
		mut(e)
		if e.Start(context.Background()) == nil {
			t.Errorf("%s: Start мав відхилити", name)
		}
	}
	e := ok()
	if _, _, err := e.Step(context.Background()); err == nil {
		t.Error("Step до Start мав дати помилку")
	}
	if e.Exhausted() != "" || e.Tokens() != 0 {
		t.Error("до Start нічого не вичерпано")
	}
	_ = os.WriteFile(surface.Path(), []byte("garbage"), 0o600)
	if err := ok().Start(context.Background()); err == nil {
		t.Error("зіпсована базова стратегія мала зупинити Start")
	}
}

func TestAuditReplayDetectsTampering(t *testing.T) {
	var buf bytes.Buffer
	l := NewAuditLog(&buf)
	_, _ = l.Append(Entry{Verdict: VerdictKeep, PrevScore: 0.1, NewScore: 0.5, Scored: true})
	_, _ = l.Append(Entry{Verdict: VerdictRefused, PrevScore: 0.5, NewScore: 0.5})
	_, _ = l.Append(Entry{Verdict: VerdictProposerFailed, PrevScore: 0.5})
	if l.Count() != 3 {
		t.Errorf("Count = %d", l.Count())
	}
	entries, err := ReadEntries(&buf)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Replay(entries)
	if err != nil || st.Keeps != 1 || st.Refusals != 1 || st.ProposerFailures != 1 || st.Final != 0.5 {
		t.Fatalf("replay = %+v, %v", st, err)
	}
	if _, err := Replay(entries[1:]); !errors.Is(err, ErrAuditBroken) {
		t.Errorf("видалений рядок не виявлено: %v", err)
	}
	bad := append([]Entry(nil), entries...)
	bad[1].PrevScore = 0.9
	if _, err := Replay(bad); !errors.Is(err, ErrAuditBroken) {
		t.Errorf("розрив метрики не виявлено: %v", err)
	}
	bad = append([]Entry(nil), entries...)
	bad[2].Verdict = "wat"
	if _, err := Replay(bad); !errors.Is(err, ErrAuditBroken) {
		t.Errorf("невідомий вердикт не виявлено: %v", err)
	}
	if _, err := Replay(nil); !errors.Is(err, ErrAuditBroken) {
		t.Error("порожній лог мав бути помилкою")
	}
	if _, err := ReadEntries(strings.NewReader("{broken\n")); err == nil {
		t.Error("пошкоджений рядок мав дати помилку")
	}
	if AsDryRun(VerdictRefused) != VerdictRefused || AsDryRun(VerdictDiscard) != VerdictWouldDiscard {
		t.Error("AsDryRun")
	}
	if NewAuditLogAfter(new(bytes.Buffer), -5).next != 1 {
		t.Error("NewAuditLogAfter(-5)")
	}
}

func TestLastEntryIDAndEnsureStrategy(t *testing.T) {
	dir := t.TempDir()
	if id, err := lastEntryID(filepath.Join(dir, "none.log")); err != nil || id != 0 {
		t.Errorf("нема журналу: %d %v", id, err)
	}
	bad := filepath.Join(dir, "bad.log")
	_ = os.WriteFile(bad, []byte("{x\n"), 0o600)
	if _, err := lastEntryID(bad); err == nil {
		t.Error("пошкоджений журнал мав зупинити")
	}
	empty := filepath.Join(dir, "empty.log")
	_ = os.WriteFile(empty, nil, 0o600)
	if id, err := lastEntryID(empty); err != nil || id != 0 {
		t.Errorf("порожній журнал: %d %v", id, err)
	}
	p := filepath.Join(dir, "s.md")
	if seeded, err := ensureStrategy(p); !seeded || err != nil {
		t.Errorf("ensureStrategy 1: %v %v", seeded, err)
	}
	if seeded, err := ensureStrategy(p); seeded || err != nil {
		t.Errorf("ensureStrategy 2: %v %v", seeded, err)
	}
	if _, err := ensureStrategy(filepath.Join(dir, "no", "dir", "s.md")); err == nil {
		t.Error("недоступна тека мала дати помилку")
	}
}

func TestUsageTokens(t *testing.T) {
	if usageTokens(nil) != 0 || usageTokens(&model.LLMResponse{}) != 0 {
		t.Error("нема usage — нуль")
	}
	if usageTokens(&model.LLMResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{TotalTokenCount: 42}}) != 42 {
		t.Error("TotalTokenCount")
	}
	u := &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 5, ThoughtsTokenCount: 1}
	if usageTokens(&model.LLMResponse{UsageMetadata: u}) != 16 {
		t.Error("сума складників")
	}
}

func TestNewSelfImproveAgentValidation(t *testing.T) {
	surface := newTestSurface(t, baselineStrategy())
	sink := NewProposalSink()
	p, err := NewProposerAgent(fakellm.New("x"), surface, sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	eng := &Engine{}
	if _, err := NewSelfImproveAgent(nil, p, 1); err == nil {
		t.Error("без Engine")
	}
	if _, err := NewSelfImproveAgent(eng, nil, 1); err == nil {
		t.Error("без пропонента")
	}
	if _, err := NewSelfImproveAgent(eng, p, 0); err == nil {
		t.Error("maxIterations=0 означає «вічно» у loopagent — має бути відхилено")
	}
	if _, err := NewSelfImproveAgent(eng, p, 2); err != nil {
		t.Errorf("коректна збірка: %v", err)
	}
}

func TestProposalSink(t *testing.T) {
	s := NewProposalSink()
	s.AddTokens(7)
	if _, err := s.Propose(context.Background(), StrategyDoc{}, nil); !errors.Is(err, ErrNoProposal) {
		t.Fatalf("порожня черга: %v", err)
	}
	s.Refuse("../x", "поза поверхнею")
	if r := s.Refusals(); len(r) != 1 || r[0].Path != "../x" {
		t.Errorf("refusals = %+v", r)
	}
	s.AddTokens(3)
	s.Offer(Proposal{Path: "strategy.md", Content: "c"})
	p, err := s.Propose(context.Background(), StrategyDoc{}, nil)
	if err != nil || p.Tokens != 3 {
		t.Errorf("Propose = %+v, %v (токени невдалої спроби вже віддано)", p, err)
	}
}

func TestWriteStrategyToolRefusesBadInput(t *testing.T) {
	surface := newTestSurface(t, baselineStrategy())
	sink := NewProposalSink()
	proposer := fakellm.New("p",
		fakellm.CallTurn("write_strategy", map[string]any{"path": "../../etc/passwd", "content": "x", "summary": "s"}),
		fakellm.CallTurn("write_strategy", map[string]any{"path": "strategy.md", "content": "", "summary": "s"}),
		fakellm.CallTurn("write_strategy", map[string]any{"path": "strategy.md", "content": "ok", "summary": "s"}),
		fakellm.TextTurn("done"))
	a, err := NewProposerAgent(proposer, surface, sink, func() string { return "brief" })
	if err != nil {
		t.Fatal(err)
	}
	runProposer(t, a)
	if len(sink.Refusals()) != 1 {
		t.Errorf("гальмо №1 мало зафіксувати одну відмову: %+v", sink.Refusals())
	}
	if p, err := sink.Propose(context.Background(), StrategyDoc{}, nil); err != nil || p.Content != "ok" {
		t.Errorf("прийнято має бути лише третю: %+v %v", p, err)
	}
	if !strings.Contains(proposerRules, "write_strategy") {
		t.Error("правила пропонента мають називати інструмент")
	}
}

func TestScorerReportsCaseFailureAndLeak(t *testing.T) {
	kb := ledgerkb.New()
	m := fakellm.New("boom", fakellm.Turn{Err: errors.New("provider down")})
	s := &LLMScorer{Model: m, KB: kb, Cases: ledgerkb.Dev()[:1]}
	meas, err := s.Score(context.Background(), baselineStrategy())
	if err == nil || !strings.Contains(err.Error(), "D1") {
		t.Errorf("помилка кейса мала піднятись з ID кейса: %v", err)
	}
	if meas.Score != 0 {
		t.Errorf("збій виміру не повинен виглядати як нуль-бал: %+v", meas)
	}
	bad := baselineStrategy()
	bad.Tools = []string{"shell"}
	if _, err := NewWorkerAgent(m, kb, bad, 0); err == nil {
		t.Error("невідомий інструмент мав дати помилку")
	}
	if clip("a b c", 3) != "a b…" {
		t.Errorf("clip = %q", clip("a b c", 3))
	}
	leak := NoGoldenLeak(ledgerkb.Dev())
	d := baselineStrategy()
	d.Raw = "answer: 15 EUR and compliance officer"
	if leak(d) == nil {
		t.Error("витік золотих фактів мав бути виявлений")
	}
	d.Raw = "search carefully"
	if leak(d) != nil {
		t.Error("чиста стратегія не має відхилятись")
	}
}

// Воркер на офлайн-моделі: інструмент реально викликається, а лічильник викликів
// моделі обмежує зациклення.
func TestWorkerUsesToolsAndCapsModelCalls(t *testing.T) {
	kb := ledgerkb.New()
	doc := baselineStrategy()
	doc.Tools = allTools
	doc.Instruction = "Open every relevant document with kb_read and quote every figure."
	s := &LLMScorer{Model: offline.Worker{}, KB: kb, Cases: ledgerkb.Dev()[:1], MaxCalls: 1}
	r := s.RunCases(context.Background(), doc)[0]
	if r.Failure != nil {
		t.Fatalf("кейс упав: %v", r.Failure)
	}
	if !strings.Contains(r.Answer, "model-call cap") {
		t.Errorf("стеля викликів моделі (1) не спрацювала: %q", r.Answer)
	}
	if Describe([]CaseResult{r}) == "" {
		t.Error("Describe порожній")
	}
}

func TestPersistLogErrors(t *testing.T) {
	dir := t.TempDir()
	cfg := LoopConfig{LogName: "a.log"}
	if err := persistLog(cfg, filepath.Join(dir, "missing"), []Entry{{}}); err == nil {
		t.Error("недоступна тека журналу мала дати помилку")
	}
	if _, _, err := workingDir(LoopConfig{ReportOnly: true, StrategyName: "s.md"}, dir, filepath.Join(dir, "s.md")); err == nil {
		t.Error("L1 без strategy.md мав дати помилку")
	}
	if _, err := RunLoop(context.Background(), LoopConfig{Root: filepath.Join(dir, "x", "y"), StrategyName: "s.md", LogName: "l", Experiments: 1, Models: offlineModels("t", "s.md")}, new(bytes.Buffer)); err == nil {
		t.Error("RunLoop у недоступній теці мав дати помилку")
	}
}
