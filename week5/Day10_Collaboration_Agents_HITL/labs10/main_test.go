// Тести ДЗ 10. Жоден не потребує ключа: топологію графа, політику затвора й
// стримування збою перевіряємо зі скриптованими моделями (internal/fakellm).
//
//	go test ./...
//	LAB_LIVE=1 go test -run Live -v .   # справжня модель з apps/.env
package main

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/adkrun"
	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/week5/Day10_Collaboration_Agents_HITL/labs10/internal/approvals"
)

func offline(context.Context) (model.LLM, string) { return nil, "ОФЛАЙН-ФЕЙК (тест)" }

type testEnv struct {
	dir string
	out *bytes.Buffer
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	return &testEnv{dir: t.TempDir(), out: &bytes.Buffer{}}
}

// open is one «process»: its own Store over the same file.
func (e *testEnv) open(t *testing.T, down ...string) *app {
	t.Helper()
	st, err := approvals.Open(filepath.Join(e.dir, "labs10.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	d := map[string]bool{}
	for _, n := range down {
		d[n] = true
	}
	return &app{
		out: e.out, in: strings.NewReader(""), store: st, label: "test",
		user: "iryna", by: "oksana", down: d, window: time.Minute,
		outbox: filepath.Join(e.dir, "outbox"), now: time.Now,
	}
}

func mustStart(t *testing.T, a *app, sid string) approvals.Record {
	t.Helper()
	id, err := a.start(context.Background(), sid)
	if err != nil {
		t.Fatalf("start: %v\n%s", err, outOf(a))
	}
	rec, err := a.store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func outOf(a *app) string { return a.out.(*bytes.Buffer).String() }

func TestRiskToolReturnsOnlySourcedFindings(t *testing.T) {
	out, err := lookupRisk(nil, deskInput{Topic: "міграція"})
	if err != nil || len(out.Findings) == 0 {
		t.Fatalf("lookupRisk: %v %+v", err, out)
	}
	for name, s := range desks {
		for _, f := range s.Fixture {
			if f.Source == "" {
				t.Errorf("%s: факт без джерела: %+v", name, f)
			}
		}
	}
}

// TestJoinNodeWaitsForAllBranches — п.2: JoinNode — бар'єр. Повільна гілка
// закінчується пізніше, а вузол після join усе одно бачить обидві й рівно раз.
func TestJoinNodeWaitsForAllBranches(t *testing.T) {
	fast := workflow.NewFunctionNode("fast", func(agent.Context, any) (string, error) {
		return "fast done", nil
	}, workflow.NodeConfig{})
	slow := workflow.NewFunctionNode("slow", func(agent.Context, any) (string, error) {
		time.Sleep(50 * time.Millisecond)
		return "slow done", nil
	}, workflow.NodeConfig{})
	var mu sync.Mutex
	var seen []map[string]any
	after := workflow.NewFunctionNode("after", func(_ agent.Context, in map[string]any) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, in)
		return "ok", nil
	}, workflow.NodeConfig{})
	join := workflow.NewJoinNode("gather")

	eb := workflow.NewEdgeBuilder()
	eb.AddFanOut(workflow.Start, fast, slow)
	eb.AddFanIn(join, fast, slow)
	eb.Add(join, after)
	root, err := workflowagent.New(workflowagent.Config{Name: "join_test", Edges: eb.Build()})
	if err != nil {
		t.Fatal(err)
	}
	r, err := runner.New(runner.Config{AppName: "t", Agent: root, SessionService: session.InMemoryService(), AutoCreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range r.Run(t.Context(), "u", "s", genai.NewContentFromText("go", genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("вузол після join запустився %d разів, want 1", len(seen))
	}
	if seen[0]["fast"] != "fast done" || seen[0]["slow"] != "slow done" {
		t.Fatalf("join віддав неповний вхід: %v", seen[0])
	}
}

// barrierModel blocks the first call of every desk until all desks arrived.
// If the branches ran one after another, the first one would time out.
type barrierModel struct {
	inner   model.LLM
	arrive  func()
	allHere <-chan struct{}
	once    sync.Once
}

func (b *barrierModel) Name() string { return b.inner.Name() }

func (b *barrierModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	var waitErr error
	b.once.Do(func() {
		b.arrive()
		select {
		case <-b.allHere:
		case <-time.After(2 * time.Second):
			waitErr = errors.New("гілки йдуть послідовно: інші спеціалісти так і не стартували")
		}
	})
	if waitErr != nil {
		return func(yield func(*model.LLMResponse, error) bool) { yield(nil, waitErr) }
	}
	return b.inner.GenerateContent(ctx, req, stream)
}

func TestDesksRunInParallel(t *testing.T) {
	a := newEnv(t).open(t)
	var wg sync.WaitGroup
	wg.Add(3)
	allHere := make(chan struct{})
	go func() { wg.Wait(); close(allHere) }()
	a.models = func() func(string) model.LLM {
		base := offlineModels(nil)
		return func(name string) model.LLM {
			if name == coordinatorName {
				return base(name)
			}
			return &barrierModel{inner: base(name), arrive: wg.Done, allHere: allHere}
		}
	}
	rec := mustStart(t, a, "parallel")
	ev, _ := evidenceOf(rec)
	if !ev.ApproveAllowed || len(ev.Sections) != 3 {
		t.Fatalf("evidence = %+v", ev)
	}
}

// fakes remembers every offline model, so a test can read what each agent saw.
func recordingModels(down map[string]bool, fakes map[string]*fakellm.Model, mu *sync.Mutex) func() func(string) model.LLM {
	return func() func(string) model.LLM {
		base := offlineModels(down)
		return func(name string) model.LLM {
			m := base(name).(*fakellm.Model)
			mu.Lock()
			fakes[name] = m
			mu.Unlock()
			return m
		}
	}
}

func TestSpecialistsDoNotSeeEachOther(t *testing.T) {
	a := newEnv(t).open(t)
	fakes := map[string]*fakellm.Model{}
	var mu sync.Mutex
	a.models = recordingModels(nil, fakes, &mu)
	mustStart(t, a, "isolation")

	for _, name := range defaultPlan().Desks {
		var seen strings.Builder
		for _, req := range fakes[name].Requests() {
			for _, c := range req.Contents {
				for _, p := range c.Parts {
					seen.WriteString(p.Text)
					if p.FunctionCall != nil {
						seen.WriteString(p.FunctionCall.Name)
					}
				}
			}
		}
		for _, other := range defaultPlan().Desks {
			if other == name {
				continue
			}
			if s := desks[other]; strings.Contains(seen.String(), s.Tool) || strings.Contains(seen.String(), s.Fixture[0].Source) {
				t.Errorf("%s бачить контекст %s:\n%s", name, other, seen.String())
			}
		}
	}
	// The coordinator gets the merged brief, so it must see all three sections.
	var coord strings.Builder
	for _, req := range fakes[coordinatorName].Requests() {
		for _, c := range req.Contents {
			for _, p := range c.Parts {
				coord.WriteString(p.Text)
			}
		}
	}
	for _, name := range defaultPlan().Desks {
		if !strings.Contains(coord.String(), name) {
			t.Errorf("координатор не отримав розділ %s", name)
		}
	}
}

// TestPauseSurvivesRestartAndApprovalIsOneShot — п.3: start в одному
// «процесі», decide в іншому; повторне відновлення не надсилає вдруге.
func TestPauseSurvivesRestartAndApprovalIsOneShot(t *testing.T) {
	env := newEnv(t)
	first := env.open(t)
	rec := mustStart(t, first, "board-1")
	_ = first.store.Close()

	second := env.open(t)
	pend, err := second.store.PendingList(t.Context())
	if err != nil || len(pend) != 1 || pend[0].InterruptID != rec.InterruptID {
		t.Fatalf("після рестарту pending = %+v, %v", pend, err)
	}
	out, err := second.decide(t.Context(), rec.InterruptID, "approve")
	if err != nil {
		t.Fatalf("decide: %v\n%s", err, env.out)
	}
	if !out.Sent {
		t.Fatalf("approve не надіслав: %+v", out)
	}
	sent, err := os.ReadFile(out.Path)
	if err != nil || hashOf(string(sent)) != rec.Hash {
		t.Fatalf("надіслано не той документ: hash %s vs %s (%v)", hashOf(string(sent)), rec.Hash, err)
	}
	got, _ := second.store.Get(t.Context(), rec.InterruptID)
	if got.DecidedBy != "oksana" || got.DecidedAt == nil || got.ConsumedAt == nil {
		t.Fatalf("журнал без хто/коли/використано: %+v", got)
	}

	again, err := second.resume(t.Context(), got, "approve")
	if err != nil {
		t.Fatal(err)
	}
	if again.Sent || !strings.Contains(again.Reason, "вже використано") {
		t.Fatalf("друге відновлення надіслало вдруге: %+v", again)
	}
	if _, err := second.decide(t.Context(), rec.InterruptID, "deny"); !errors.Is(err, approvals.ErrAlreadyDecided) {
		t.Fatalf("друге рішення: err = %v, want ErrAlreadyDecided", err)
	}
}

func TestDenyDoesNotSend(t *testing.T) {
	a := newEnv(t).open(t)
	rec := mustStart(t, a, "deny")
	out, err := a.decide(t.Context(), rec.InterruptID, "deny")
	if err != nil {
		t.Fatal(err)
	}
	if out.Sent || out.Status != approvals.Denied {
		t.Fatalf("deny: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(a.outbox, "board-pack.md")); !os.IsNotExist(err) {
		t.Fatalf("після deny документ у outbox: %v", err)
	}
}

// TestSpecialistFailureDoesNotCorruptTheAnswer — п.6: integration_desk мовчить,
// а фейкова модель пише «Усе гаразд». Розділ стає прогалиною з кодом, доказ
// її показує, approve заблоковано, документ не містить вигаданого.
func TestSpecialistFailureDoesNotCorruptTheAnswer(t *testing.T) {
	a := newEnv(t).open(t, integrationDesk)
	rec := mustStart(t, a, "gap")

	ev, err := evidenceOf(rec)
	if err != nil {
		t.Fatal(err)
	}
	var integ SectionStatus
	for _, s := range ev.Sections {
		if s.Desk == integrationDesk {
			integ = s
		}
	}
	if integ.Status != "unverified" || integ.Code != "INTEGRATION_TIMEOUT" {
		t.Fatalf("розділ integration_desk = %+v, want unverified/INTEGRATION_TIMEOUT", integ)
	}
	if ev.ApproveAllowed || !slices.Contains(ev.Gaps, integrationDesk) {
		t.Fatalf("прогалини не видно в доказі: %+v", ev)
	}
	if !strings.Contains(outOf(a), "ПРОГАЛИНИ:  integration_desk") {
		t.Errorf("людина не бачить прогалину у виводі:\n%s", outOf(a))
	}

	doc, err := os.ReadFile(rec.ArtifactURI)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "ПРОГАЛИНА (INTEGRATION_TIMEOUT)") {
		t.Errorf("документ без прогалини:\n%s", doc)
	}
	for _, lie := range []string{"Усе гаразд", "37 мерчантів", "crm:A-114"} {
		if strings.Contains(string(doc), lie) {
			t.Errorf("документ містить %q, хоча джерело мовчало", lie)
		}
	}

	if _, err := a.decide(t.Context(), rec.InterruptID, "approve"); !errors.Is(err, ErrUnverifiedSection) {
		t.Fatalf("approve з прогалиною: err = %v, want ErrUnverifiedSection", err)
	}
	if got, _ := a.store.Get(t.Context(), rec.InterruptID); got.Status != approvals.Pending {
		t.Fatalf("заблокований approve щось записав: %s", got.Status)
	}

	// Even an approval that bypassed decide cannot send a document with a gap.
	e := &Engine{Store: a.store, Outbox: a.outbox}
	var d Draft
	d.Sections = []Section{{Desk: integrationDesk}}
	out, err := e.notifyBoard(adkrun.NewTestContext(t.Context()), GateResult{Status: approvals.Approved, Draft: d})
	if err != nil || out.Sent || !strings.Contains(out.Reason, integrationDesk) {
		t.Fatalf("notifyBoard з прогалиною: %+v, %v", out, err)
	}

	out, err = a.decide(t.Context(), rec.InterruptID, "deny")
	if err != nil || out.Sent {
		t.Fatalf("deny після прогалини: %+v, %v", out, err)
	}
}

func TestAllSourcesDownGivesNoRequest(t *testing.T) {
	a := newEnv(t).open(t, riskDesk, complianceDesk, integrationDesk)
	_, err := a.start(t.Context(), "all-down")
	if !errors.Is(err, ErrNoEvidence) {
		t.Fatalf("err = %v, want ErrNoEvidence", err)
	}
	for _, code := range []string{"INCIDENT_DB_TIMEOUT", "COMPLIANCE_REGISTER_TIMEOUT", "INTEGRATION_TIMEOUT"} {
		if !strings.Contains(err.Error(), code) {
			t.Errorf("помилка не називає %s: %v", code, err)
		}
	}
	if pend, _ := a.store.PendingList(t.Context()); len(pend) != 0 {
		t.Fatalf("запит без доказу потрапив у журнал: %+v", pend)
	}
}

func TestCoordinatorHidingAGapIsFlagged(t *testing.T) {
	a := newEnv(t).open(t, integrationDesk)
	a.models = func() func(string) model.LLM {
		base := offlineModels(a.down)
		return func(name string) model.LLM {
			if name == coordinatorName {
				return fakellm.New("smooth", fakellm.TextTurn("Усе готово до міграції (incident-db:INC-418)."))
			}
			return base(name)
		}
	}
	rec := mustStart(t, a, "smooth")
	ev, _ := evidenceOf(rec)
	if !slices.Contains(ev.UnnamedGaps, integrationDesk) {
		t.Fatalf("приховану прогалину не позначено: %+v", ev)
	}
	if !strings.Contains(outOf(a), "не називає прогалину integration_desk") {
		t.Errorf("людина не бачить попередження:\n%s", outOf(a))
	}
}

// TestApprovalTimeoutMeansRefusal — п.4: кожен шлях без чіткого «так» — відмова
// зі своєю причиною; перевіряємо через errors.Is.
func TestApprovalTimeoutMeansRefusal(t *testing.T) {
	never := make(chan string) // nobody ever writes here
	closed := make(chan string)
	close(closed)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	word := func(w string) <-chan string {
		ch := make(chan string, 1)
		ch <- w
		return ch
	}

	tests := []struct {
		name    string
		ctx     context.Context
		replies <-chan string
		wantErr error
		status  approvals.Status
		by      string
	}{
		{"мовчання", t.Context(), never, ErrDecisionTimeout, approvals.TimedOut, "system:timeout"},
		{"скасований контекст", cancelled, never, ErrDecisionCancelled, approvals.TimedOut, "system:cancel"},
		{"закритий канал", t.Context(), closed, ErrRepliesClosed, approvals.TimedOut, "system:ui"},
		{"нерозпізнане слово", t.Context(), word("може"), ErrUnrecognizedReply, approvals.Invalid, "oksana"},
		{"людина відмовила", t.Context(), word("deny"), nil, approvals.Denied, "oksana"},
		{"людина схвалила", t.Context(), word("approve"), nil, approvals.Approved, "oksana"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			d, err := awaitDecision(tt.ctx, tt.replies, 30*time.Millisecond, "oksana")
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if d.Status != tt.status || d.By != tt.by || d.Reason == "" {
				t.Fatalf("decision = %+v, want %s by %s з причиною", d, tt.status, tt.by)
			}
			if time.Since(start) > time.Second {
				t.Fatalf("очікування тривало %s", time.Since(start))
			}
		})
	}
}

func TestSilenceEndsTheRunAsTimedOut(t *testing.T) {
	a := newEnv(t).open(t)
	a.window = 30 * time.Millisecond
	rec := mustStart(t, a, "silence")
	time.Sleep(60 * time.Millisecond)

	outs, err := a.sweep(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 1 || outs[0].Sent || outs[0].Status != approvals.TimedOut {
		t.Fatalf("sweep = %+v", outs)
	}
	got, _ := a.store.Get(t.Context(), rec.InterruptID)
	if got.DecidedBy != "system:timeout" || !strings.Contains(got.Reason, "ніхто не відповів") {
		t.Fatalf("таймаут без названої причини: %+v", got)
	}
	if _, err := a.decide(t.Context(), rec.InterruptID, "approve"); !errors.Is(err, approvals.ErrAlreadyDecided) {
		t.Fatalf("пізній approve: err = %v", err)
	}
}

// Refused, nobody there, unreadable reply: one consequence, three records.
func TestThreeRefusalsAreThreeRecords(t *testing.T) {
	a := newEnv(t).open(t)
	a.window = 30 * time.Millisecond
	denied := mustStart(t, a, "r-deny")
	silent := mustStart(t, a, "r-silent")
	garbage := mustStart(t, a, "r-garbage")
	a.window = time.Minute

	if _, err := a.decide(t.Context(), denied.InterruptID, "deny"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.decide(t.Context(), garbage.InterruptID, "👍"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := a.sweep(t.Context()); err != nil {
		t.Fatal(err)
	}

	want := map[string]approvals.Status{
		denied.InterruptID: approvals.Denied, silent.InterruptID: approvals.TimedOut, garbage.InterruptID: approvals.Invalid,
	}
	reasons := map[string]bool{}
	for id, status := range want {
		got, _ := a.store.Get(t.Context(), id)
		if got.Status != status {
			t.Errorf("%s: %s, want %s", id, got.Status, status)
		}
		reasons[got.Reason] = true
	}
	if len(reasons) != 3 {
		t.Errorf("причини не різні: %v", reasons)
	}
}

func TestResumeWithoutLedgerDecisionIsInvalid(t *testing.T) {
	a := newEnv(t).open(t)
	rec := mustStart(t, a, "bypass")
	out, err := a.resume(t.Context(), rec, "approve")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := a.store.Get(t.Context(), rec.InterruptID)
	if out.Sent || got.Status != approvals.Invalid || got.DecidedBy != "system:gate" {
		t.Fatalf("відповідь в обхід журналу: out=%+v rec=%+v", out, got)
	}
}

// mapState is a session.State over a map, for Merge without a runner.
type mapState map[string]any

func (m mapState) Get(k string) (any, error) {
	if v, ok := m[k]; ok {
		return v, nil
	}
	return nil, session.ErrStateKeyNotExist
}
func (m mapState) Set(k string, v any) error { m[k] = v; return nil }
func (m mapState) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		for k, v := range m {
			if !yield(k, v) {
				return
			}
		}
	}
}

type stateCtx struct {
	*adkrun.TestContext
	st mapState
}

func (c stateCtx) State() session.State { return c.st }

func TestMergeKeepsPlanOrderAndNamesEveryGap(t *testing.T) {
	st := mapState{
		outcomeKey(riskDesk):        `{"ok":true,"findings":[{"claim":"c","source":"s"}]}`,
		outcomeKey(complianceDesk):  `{"ok":true,"findings":[{"claim":"без джерела","source":" "}]}`,
		outcomeKey(integrationDesk): `not json`,
		outcomeKey("extra_desk"):    `{"ok":true}`,
	}
	e := &Engine{Plan: Plan{Desks: []string{integrationDesk, "missing_desk", complianceDesk, riskDesk, "extra_desk"}}}
	b, err := e.Merge(stateCtx{adkrun.NewTestContext(t.Context()), st}, map[string]any{riskDesk: " проза "})
	if err != nil {
		t.Fatal(err)
	}
	var order, codes []string
	for _, s := range b.Sections {
		order = append(order, s.Desk)
		codes = append(codes, s.Code)
	}
	if !slices.Equal(order, e.Plan.Desks) {
		t.Fatalf("порядок %v, want план %v", order, e.Plan.Desks)
	}
	if want := []string{"BAD_OUTCOME", "NO_TOOL_CALL", "UNSOURCED", "", "NO_FINDINGS"}; !slices.Equal(codes, want) {
		t.Fatalf("коди %v, want %v", codes, want)
	}
	if b.Sections[3].Prose != "проза" || !b.Sections[3].Verified {
		t.Fatalf("risk_desk = %+v", b.Sections[3])
	}
}

func TestNotifyBoardReturnsTheApprovalWhenSendFails(t *testing.T) {
	a := newEnv(t).open(t)
	ctx := t.Context()
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	doc := "doc"
	recips := defaultRecipients()
	if _, err := a.store.Ask(ctx, approvals.Request{InterruptID: "x", SessionID: "s", Hash: hashOf(doc), Recipients: recips}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.Decide(ctx, "x", approvals.Decision{Status: approvals.Approved, By: "oksana"}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: a.store, Outbox: filepath.Join(blocker, "outbox")}
	g := GateResult{InterruptID: "x", Status: approvals.Approved, By: "oksana",
		Draft: Draft{Document: doc, Recipients: recips, Sections: []Section{{Desk: riskDesk, Verified: true}}}}
	if _, err := e.notifyBoard(adkrun.NewTestContext(ctx), g); err == nil {
		t.Fatal("відправка в неможливий outbox пройшла")
	}
	got, _ := a.store.Get(ctx, "x")
	if got.ConsumedAt != nil || !strings.Contains(got.Reason, "відправка впала") {
		t.Fatalf("схвалення не повернуто після збою: %+v", got)
	}
	// A changed document cannot reuse the approval.
	g.Draft.Document = "doc v2"
	e.Outbox = a.outbox
	out, err := e.notifyBoard(adkrun.NewTestContext(ctx), g)
	if err != nil || out.Sent || !strings.Contains(out.Reason, "документ змінився") {
		t.Fatalf("змінений документ: %+v, %v", out, err)
	}
}

func TestAuditShowsPauseResumeBranchesAndLedger(t *testing.T) {
	a := newEnv(t).open(t, integrationDesk)
	rec := mustStart(t, a, "audit-me")
	if _, err := a.decide(t.Context(), rec.InterruptID, "deny"); err != nil {
		t.Fatal(err)
	}
	a.out.(*bytes.Buffer).Reset()
	if err := a.audit(t.Context(), "audit-me"); err != nil {
		t.Fatal(err)
	}
	got := outOf(a)
	for _, want := range []string{
		"risk_desk@1", "compliance_desk@1", "integration_desk@1",
		"ПОМИЛКА INTEGRATION_TIMEOUT", "вихід gather", "integration_desk=GAP INTEGRATION_TIMEOUT",
		"ПАУЗА: запит людині " + rec.InterruptID, "ВІДНОВЛЕННЯ: відповідь deny",
		"вихід approval_gate: status=denied by=oksana", "вихід notify_board: sent=false",
		"== журнал рішень (1)", "→ denied", "модель не повернула UsageMetadata",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("audit без %q:\n%s", want, got)
		}
	}
	if err := a.audit(t.Context(), "no-such"); err == nil {
		t.Error("audit неіснуючої сесії без помилки")
	}
}

func TestPrintTokensSumsPerAuthor(t *testing.T) {
	ev := func(author string, p, o, total int32) *session.Event {
		e := session.NewEvent(adkrun.NewTestContext(t.Context()), "inv")
		e.Author = author
		e.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: p, CandidatesTokenCount: o, TotalTokenCount: total}
		return e
	}
	var buf bytes.Buffer
	printTokens(&buf, []*session.Event{ev("risk_desk", 10, 2, 12), ev("coordinator", 5, 5, 11), ev("risk_desk", 1, 1, 2), {Author: "x"}}, false)
	got := buf.String()
	for _, want := range []string{"risk_desk        викликів=2 prompt=11 output=3 total=14", "coordinator", "РАЗОМ            prompt=16 output=8 total=25"} {
		if !strings.Contains(got, want) {
			t.Errorf("немає %q:\n%s", want, got)
		}
	}
	buf.Reset()
	printTokens(&buf, nil, true)
	if !strings.Contains(buf.String(), "офлайн-фейк") {
		t.Errorf("офлайн без чесної причини: %s", buf.String())
	}
}

func TestLineDiff(t *testing.T) {
	if got := lineDiff("", "a"); !strings.Contains(got[0], "перша версія") {
		t.Errorf("перша версія: %v", got)
	}
	if got := lineDiff("a\nb", "a\nb"); !strings.Contains(got[0], "без змін") {
		t.Errorf("без змін: %v", got)
	}
	if got := lineDiff("a\nb", "a\nc"); !slices.Equal(got, []string{"- b", "+ c"}) {
		t.Errorf("діф: %v", got)
	}
}

func TestRunCommands(t *testing.T) {
	dir := t.TempDir()
	flags := []string{"-db", filepath.Join(dir, "db.sqlite"), "-outbox", filepath.Join(dir, "out")}
	cli := func(stdin string, args ...string) (string, error) {
		var out bytes.Buffer
		err := run(t.Context(), append(slices.Clone(flags), args...), strings.NewReader(stdin), &out, offline)
		return out.String(), err
	}

	out, err := cli("", "-session", "cli-1", "start")
	if err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	if _, err := cli("", "-session", "cli-1", "start"); err == nil {
		t.Error("другий start у ту саму сесію пройшов")
	}
	out, err = cli("", "pending")
	if err != nil || !strings.Contains(out, "ЗАПИТ НА СХВАЛЕННЯ") {
		t.Fatalf("pending: %v\n%s", err, out)
	}
	id := strings.Fields(out[strings.Index(out, "board-pack-"):])[0]
	if out, err = cli("", "decide", id, "approve"); err != nil || !strings.Contains(out, "дія ВИКОНАНО") {
		t.Fatalf("decide: %v\n%s", err, out)
	}
	if out, _ = cli("", "pending"); !strings.Contains(out, "немає запитів") {
		t.Errorf("pending після рішення:\n%s", out)
	}
	if out, err = cli("", "sweep"); err != nil || !strings.Contains(out, "прострочених запитів немає") {
		t.Errorf("sweep: %v\n%s", err, out)
	}
	if out, err = cli("", "audit", "cli-1"); err != nil || !strings.Contains(out, "notify_board: sent=true") {
		t.Errorf("audit: %v\n%s", err, out)
	}

	out, err = cli("approve\n", "-session", "cli-demo", "demo")
	if err != nil || !strings.Contains(out, "дія ВИКОНАНО") {
		t.Fatalf("demo approve: %v\n%s", err, out)
	}
	out, err = cli("approve\nmaybe\n", "-session", "cli-demo-gap", "-down", "integration_desk", "demo")
	if err != nil || !strings.Contains(out, "approve заблоковано") || !strings.Contains(out, "→ invalid") {
		t.Fatalf("demo з прогалиною: %v\n%s", err, out)
	}
	out, err = cli("", "-session", "cli-demo-eof", "demo")
	if err != nil || !strings.Contains(out, "канал відповідей закрито") {
		t.Fatalf("demo без stdin: %v\n%s", err, out)
	}

	for _, bad := range [][]string{{"decide", "x"}, {"audit"}, {"nope"}, {"decide", "no-such", "approve"}, {"-bad-flag"}} {
		if _, err := cli("", bad...); err == nil {
			t.Errorf("%v: без помилки", bad)
		}
	}
}

func TestParseDown(t *testing.T) {
	got := parseDown(" integration_desk, ,risk_desk")
	if !got[integrationDesk] || !got[riskDesk] || len(got) != 2 {
		t.Fatalf("parseDown = %v", got)
	}
}

func TestNewEngineRejectsBadConfig(t *testing.T) {
	if _, err := NewEngine(&Engine{}); err == nil {
		t.Error("engine без Store і Model")
	}
	a := newEnv(t).open(t)
	_, err := NewEngine(&Engine{Store: a.store, Model: offlineModels(nil), Plan: Plan{Desks: []string{"ghost_desk"}}})
	if err == nil || !strings.Contains(err.Error(), "ghost_desk") {
		t.Errorf("невідомий спеціаліст у плані: %v", err)
	}
}

// TestLiveTeamGroundsSectionsAndContainsTheGap runs the real model from
// apps/.env. Without LAB_LIVE=1 or without a provider it skips and says why.
func TestLiveTeamGroundsSectionsAndContainsTheGap(t *testing.T) {
	if os.Getenv("LAB_LIVE") != "1" {
		t.Skip("живий тест: задайте LAB_LIVE=1 (потрібен провайдер у apps/.env)")
	}
	m, label := resolveModel(t.Context())
	if m == nil {
		t.Skip("LAB_LIVE=1, але провайдера немає: " + label)
	}
	t.Log(label)
	a := newEnv(t).open(t, integrationDesk)
	a.live = m
	rec := mustStart(t, a, "live")
	ev, err := evidenceOf(rec)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ev.Sections {
		want := "verified"
		if s.Desk == integrationDesk {
			want = "unverified"
		}
		if s.Status != want {
			t.Errorf("%s: %s, want %s (модель не викликала інструмент?)", s.Desk, s.Status, want)
		}
	}
	if ev.ApproveAllowed {
		t.Error("approve дозволено попри прогалину")
	}
	t.Log(outOf(a))
}
