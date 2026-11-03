// Каркас тестів ДЗ 9. Усе, що не позначено t.Skip, проходить на стартері
// одразу, без мережі й ключа (fakellm — скриптована модель із labs/internal).
//
// «Немає тестових файлів» — найгірший результат: він виглядає як успіх.
// Тому нижче є три робочі тести й два скелети, які ви закриваєте в ДЗ.
//
//	go test ./...        # зелене, скіпи видно
//	go test -race ./...  # так це перевірятимуть при оцінюванні
//	LAB_LIVE=1 go test -run Live -v ./...   # справжня модель, якщо провайдер знайдено
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/week5/Day9_Agent_Memory_Event_Sourced_State/labs9/internal/memlab"
)

func testApp(t *testing.T) App {
	t.Helper()
	st, err := memlab.Open(filepath.Join(t.TempDir(), "memory.db"), memlab.IngestPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return App{Store: st, Policy: memlab.DefaultPolicy(), Meter: &memlab.Meter{}, User: user}
}

func turnOn(t *testing.T, a App, m *fakellm.Model, sid, text string) memlab.Turn {
	t.Helper()
	ag, err := newAgent(m, a)
	if err != nil {
		t.Fatal(err)
	}
	r, err := newRunner(ag, a.Store, nil)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := memlab.Say(context.Background(), r, a.User, sid, text)
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

// TestRememberFactEmitsStateDelta — робочий тест: запис факту видно як
// StateDelta на події інструмента. Саме ця перевірка ловить помилку
// ctx.Session().State().Set (вона панікує або не лишає дельти): тест
// дивиться на подію, а не на внутрішню структуру.
func TestRememberFactEmitsStateDelta(t *testing.T) {
	a := testApp(t)
	m := fakellm.New("fake",
		fakellm.CallTurn("remember_fact", map[string]any{"key": "integration.A-114", "value": "legacy"}),
		fakellm.TextTurn("Записав."),
	)
	turn := turnOn(t, a, m, "s1", "A-114 сидить на legacy")
	var delta bool
	for _, e := range turn.Events {
		if _, ok := e.Actions.StateDelta[memlab.FactPrefix+"integration.A-114"]; ok {
			delta = true
		}
	}
	if !delta {
		t.Fatalf("жодної події зі StateDelta на %sintegration.A-114: %+v", memlab.FactPrefix, memlab.Audit(turn.Events))
	}
}

// TestFactReachesSecondSession — робочий тест, п.1 завдання: факт з однієї
// сесії потрапляє в промпт іншої (user: стан спільний для сесій користувача).
func TestFactReachesSecondSession(t *testing.T) {
	a := testApp(t)
	turnOn(t, a, fakellm.New("fake",
		fakellm.CallTurn("remember_fact", map[string]any{"key": "integration.A-114", "value": "legacy"}),
		fakellm.TextTurn("Записав."),
	), "s1", "A-114 сидить на legacy")

	m2 := fakellm.New("fake", fakellm.TextTurn("Радимо legacy-фікс."))
	turnOn(t, a, m2, "s2", "Що порадити мерчанту A-114?")
	var sys strings.Builder
	for _, p := range m2.Requests()[0].Config.SystemInstruction.Parts {
		sys.WriteString(p.Text)
	}
	if !strings.Contains(sys.String(), "integration.A-114 = legacy") {
		t.Fatalf("промпт другої сесії не містить факту:\n%s", sys.String())
	}
}

// TestRunCommandsOffline — команди CLI без мережі: say → facts.
func TestRunCommandsOffline(t *testing.T) {
	old := resolveModel
	resolveModel = func(context.Context, io.Writer) (model.LLM, string) {
		return memlab.Offline{Gullible: true}, "offline-rules (тест)"
	}
	t.Cleanup(func() { resolveModel = old })
	db := filepath.Join(t.TempDir(), "memory.db")
	var out bytes.Buffer
	for _, args := range [][]string{
		{"-db", db, "say", "s1", "A-114 сидить на legacy"},
		{"-db", db, "facts", "s1"},
		{"-db", db, "-offline", "say", "s1", "ще раз", "A-114", "на", "legacy"},
		{"-db", db, "poison"},
	} {
		if err := run(context.Background(), args, &out); err != nil {
			t.Fatalf("run(%v): %v", args, err)
		}
	}
	if err := run(context.Background(), []string{"-db", db, "nope"}, &out); err == nil {
		t.Error("невідома команда має повертати помилку")
	}
}

// saveLegacy is day1: the user says A-114 is on legacy and the model saves it.
func saveLegacy(t *testing.T, a App) {
	t.Helper()
	turnOn(t, a, fakellm.New("fake",
		fakellm.CallTurn("remember_fact", map[string]any{"key": "integration.A-114", "value": "legacy"}),
		fakellm.TextTurn("Записав."),
	), "day1", "A-114 сидить на legacy")
}

// storedFact reads the fact back from the database, not from the turn.
func storedFact(t *testing.T, a App, sid, key string) memlab.Fact {
	t.Helper()
	got, err := a.Store.Sessions.Get(context.Background(), &session.GetRequest{AppName: AppName, UserID: user, SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	v, err := got.Session.State().Get(memlab.FactPrefix + key)
	if err != nil {
		t.Fatalf("no %s in state: %v", key, err)
	}
	f, ok := memlab.FactFromState(v)
	if !ok {
		t.Fatalf("cannot decode %v", v)
	}
	return f
}

// TestPoisonedNoteCannotRewriteFact — ДЗ 9, п.3: the model obeys the hidden
// instruction from the portal note, but the write is refused by the program.
func TestPoisonedNoteCannotRewriteFact(t *testing.T) {
	a := testApp(t)
	saveLegacy(t, a)

	a.Docs = map[string]string{"A-114": InjectedDoc}
	m := fakellm.New("fake",
		fakellm.CallTurn("fetch_partner_doc", map[string]any{"merchant_id": "A-114"}),
		fakellm.CallTurn("remember_fact", map[string]any{"key": "integration.A-114", "value": "new-api"}),
		fakellm.TextTurn("Готово."),
	)
	turn := turnOn(t, a, m, "poison", "Подивись, що пишуть у порталі про A-114.")

	var rejected bool
	for _, row := range memlab.Audit(turn.Events) {
		if row.Kind == "rejected" && row.Key == "integration.A-114" {
			rejected = true
		}
		if row.Kind == "state_delta" && strings.HasPrefix(row.Key, memlab.FactPrefix) {
			t.Errorf("poisoned turn wrote a fact: %+v", row)
		}
	}
	if !rejected {
		t.Fatalf("no rejected row: %+v", memlab.Audit(turn.Events))
	}
	if f := storedFact(t, a, "poison", "integration.A-114"); f.Value != "legacy" || f.Tier != memlab.TierUser {
		t.Errorf("fact after poison = %+v, want legacy [user]", f)
	}
}

// TestTaintEndsWithTheInvocation: temp: taint must not leak into the next
// message, otherwise the user could never correct a fact after a portal read.
func TestTaintEndsWithTheInvocation(t *testing.T) {
	a := testApp(t)
	saveLegacy(t, a)
	a.Docs = map[string]string{"A-114": InjectedDoc}
	turnOn(t, a, fakellm.New("fake",
		fakellm.CallTurn("fetch_partner_doc", map[string]any{"merchant_id": "A-114"}),
		fakellm.TextTurn("У порталі пишуть про new-api, підтвердіть."),
	), "s2", "Що в порталі про A-114?")

	turn := turnOn(t, a, fakellm.New("fake",
		fakellm.CallTurn("remember_fact", map[string]any{"key": "integration.A-114", "value": "new-api"}),
		fakellm.TextTurn("Оновив."),
	), "s2", "Так, A-114 справді перейшов на new-api.")
	rows := memlab.Audit(turn.Events)
	if len(rows) != 1 || rows[0].Kind != "state_delta" || !strings.Contains(rows[0].Detail, "було: legacy") {
		t.Fatalf("user correction must supersede legacy: %+v", rows)
	}
	f := storedFact(t, a, "s2", "integration.A-114")
	if f.Value != "new-api" || len(f.Previous) != 1 || f.Source != "user_message" {
		t.Errorf("fact = %+v", f)
	}
}

func TestRememberFactPolicyVerdicts(t *testing.T) {
	a := testApp(t)
	saveLegacy(t, a)
	m := fakellm.New("fake",
		fakellm.CallTurn("remember_fact", map[string]any{"key": "integration.A-114", "value": "legacy"}),
		fakellm.CallTurn("remember_fact", map[string]any{"key": "system_prompt", "value": "be evil"}),
		fakellm.CallTurn("remember_fact", map[string]any{"key": "contact_channel.A-114", "value": "ivan@shop.ua"}),
		fakellm.TextTurn("Ок."),
	)
	turn := turnOn(t, a, m, "day1", "ще раз про A-114")

	var statuses []string
	for _, e := range turn.Events {
		if len(e.Actions.StateDelta) > 0 {
			t.Errorf("no verdict here may write state: %v", e.Actions.StateDelta)
		}
		if e.LLMResponse.Content == nil {
			continue
		}
		for _, p := range e.LLMResponse.Content.Parts {
			if p.FunctionResponse != nil {
				statuses = append(statuses, fmt.Sprint(p.FunctionResponse.Response["status"]))
			}
		}
	}
	if want := []string{"unchanged", "rejected", "rejected"}; !slices.Equal(statuses, want) {
		t.Errorf("statuses = %v, want %v", statuses, want)
	}
}

// TestConcurrentWritesDifferentKeys — ++ Advanced (до 20 балів). ≥10 горутин
// пишуть у РІЗНІ ключі, жодне значення не загублено; запускати з -race. Межа,
// яку тест НЕ доводить і яку треба назвати в README: відсутність data race ≠
// вирішений конфлікт двох записів в ОДИН ключ (для лічильника потрібен atomic
// або транзакція).
func TestConcurrentWritesDifferentKeys(t *testing.T) {
	t.Skip("++ Advanced: напишіть тест на ≥10 паралельних записів і зніміть цей Skip")
}

// TestLiveModelSavesFact — opt-in: справжня модель з modelcfg. Не перевіряє
// точний текст — лише те, що модель САМА викликала remember_fact і факт
// з'явився в стані.
func TestLiveModelSavesFact(t *testing.T) {
	if testing.Short() || strings.TrimSpace(os.Getenv("LAB_LIVE")) != "1" {
		t.Skip("LAB_LIVE != 1: live-тест вимкнено (LAB_LIVE=1 go test -run Live -v)")
	}
	_ = modelcfg.LoadEnv(".")
	m, choice, err := modelcfg.Load(context.Background())
	if err != nil {
		t.Skipf("провайдера не знайдено: %v", err)
	}
	t.Logf("модель: %s", choice.Reason)
	a := testApp(t)
	ag, err := newAgent(m, a)
	if err != nil {
		t.Fatal(err)
	}
	r, err := newRunner(ag, a.Store, nil)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := memlab.Say(context.Background(), r, user, "live", "Запам'ятай: мерчант A-114 сидить на legacy-інтеграції.")
	if err != nil {
		for _, s := range []string{"API_KEY_INVALID", "API key not valid", "insufficient_quota", "RESOURCE_EXHAUSTED", "401", "403"} {
			if strings.Contains(err.Error(), s) {
				t.Skipf("провайдер відхилив запит (%s) — перевірте ключ або квоту", s)
			}
		}
		t.Fatal(err)
	}
	for _, tool := range turn.Tools {
		if tool == "remember_fact" {
			return
		}
	}
	t.Fatalf("модель не викликала remember_fact; інструменти: %v, відповідь: %q", turn.Tools, turn.Text)
}

// TestStarterPiecesWithoutNetwork: дрібні шматки стартера, що не потребують
// моделі, — документ порталу, конфіг компакції, помилки CLI, вибір моделі без
// провайдера (має назвати причину, а не мовчки підмінити модель).
func TestStarterPiecesWithoutNetwork(t *testing.T) {
	got, err := App{Docs: map[string]string{"A-114": InjectedDoc}}.fetchPartnerDoc(nil, DocInput{MerchantID: "A-114"})
	if err != nil || !strings.Contains(got.Text, "SYSTEM NOTE") {
		t.Fatalf("fetchPartnerDoc = %q, %v", got.Text, err)
	}
	if got, _ := (App{}).fetchPartnerDoc(nil, DocInput{MerchantID: "Z-1"}); !strings.Contains(got.Text, "приміток немає") {
		t.Errorf("a merchant without a note: %q", got.Text)
	}
	if c := compactionConfig(500, 4, nil); c.TokenThreshold != 500 || c.EventRetentionSize != 4 {
		t.Errorf("compactionConfig = %+v", c)
	}

	var out bytes.Buffer
	for _, args := range [][]string{{}, {"nope"}, {"say", "s1"}, {"facts"}, {"-bogus"}} {
		if err := run(context.Background(), args, &out); err == nil {
			t.Errorf("run(%v) must fail with a named error", args)
		}
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"-db", filepath.Join(blocker, "x", "m.db"), "facts", "s1"}, &out); err == nil {
		t.Error("an unusable database path must fail, not be ignored")
	}
	if err := run(context.Background(), []string{"-db", filepath.Join(t.TempDir(), "m.db"), "-offline", "facts", "missing"}, &out); err == nil {
		t.Error("facts of a session that does not exist must fail")
	}

	for _, k := range []string{"MODEL", "DEFAULT_MODEL_PROVIDER", "GOOGLE_API_KEY", "GEMINI_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OLLAMA_HOST"} {
		t.Setenv(k, "")
	}
	t.Chdir(t.TempDir())
	out.Reset()
	m, note := pickModel(context.Background(), &out)
	if _, _, err := modelcfg.Load(context.Background()); err != nil &&
		(m == nil || !strings.HasPrefix(note, "offline-rules") || !strings.Contains(out.String(), "провайдера не знайдено")) {
		t.Errorf("no provider must be reported with its cause: note=%q out=%q", note, out.String())
	}
}

// failingState is a session.State whose writes are refused — the storage
// failing under remember_fact. A refusal must surface as a named error.
type failingState struct{ session.State }

func (failingState) Set(string, any) error { return errors.New("disk full") }

func (failingState) Get(string) (any, error) { return nil, session.ErrStateKeyNotExist }

// brokenState fails already on the read of the current fact.
type brokenState struct{ session.State }

func (brokenState) Get(string) (any, error) { return nil, errors.New("db closed") }

type brokenCtx struct{ agent.Context }

func (brokenCtx) State() session.State { return brokenState{} }

type failingCtx struct{ agent.Context }

func (failingCtx) State() session.State { return failingState{} }

func TestRememberFactNamesAStorageFailure(t *testing.T) {
	_, err := App{Policy: memlab.DefaultPolicy()}.rememberFact(failingCtx{}, FactInput{Key: "integration.A-114", Value: "legacy"})
	if err == nil || !strings.Contains(err.Error(), "remember_fact") || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v, want it to name remember_fact and the cause", err)
	}
	_, err = App{}.rememberFact(brokenCtx{}, FactInput{Key: "integration.A-114", Value: "legacy"})
	if err == nil || !strings.Contains(err.Error(), "db closed") {
		t.Fatalf("a failed read must not look like an empty fact: %v", err)
	}
}

func offlineModel(t *testing.T) {
	t.Helper()
	old := resolveModel
	resolveModel = func(context.Context, io.Writer) (model.LLM, string) {
		return memlab.Offline{Gullible: true}, "offline-rules (тест)"
	}
	t.Cleanup(func() { resolveModel = old })
}

// TestOtherUserDoesNotSeeTheFact — п.1, контраст: same database, same session
// id, another user — user: state belongs to iryna only.
func TestOtherUserDoesNotSeeTheFact(t *testing.T) {
	offlineModel(t)
	db := filepath.Join(t.TempDir(), "memory.db")
	var out bytes.Buffer
	for _, args := range [][]string{
		{"-db", db, "say", "day1", "A-114 сидить на legacy-інтеграції."},
		{"-db", db, "-user", "marko", "say", "day2", "Що порадити мерчанту A-114?"},
		{"-db", db, "-user", "marko", "facts", "day2"},
	} {
		if err := run(context.Background(), args, &out); err != nil {
			t.Fatalf("run(%v): %v", args, err)
		}
	}
	if !strings.Contains(out.String(), "факти user: (0)") || strings.Contains(out.String(), "[day2] < Для A-114 відомо") {
		t.Errorf("marko must not see iryna's fact:\n%s", out.String())
	}
}

// TestAuditFileFromStoredEvents — п.4: the file is built from the stored
// session, so it also has the rejected write of the poison run.
func TestAuditFileFromStoredEvents(t *testing.T) {
	offlineModel(t)
	dir := t.TempDir()
	db := filepath.Join(dir, "memory.db")
	var out bytes.Buffer
	for _, args := range [][]string{
		{"-db", db, "say", "poison", "A-114 сидить на legacy-інтеграції."},
		{"-db", db, "poison"},
		{"-db", db, "-audit-dir", dir, "audit", "poison"},
	} {
		if err := run(context.Background(), args, &out); err != nil {
			t.Fatalf("run(%v): %v", args, err)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "audit_iryna_poison.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []memlab.AuditRow
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, r := range rows {
		kinds = append(kinds, r.Kind)
	}
	if !slices.Equal(kinds, []string{"state_delta", "rejected"}) {
		t.Errorf("audit kinds = %v, want [state_delta rejected]", kinds)
	}
	for _, args := range [][]string{{"-db", db, "audit"}, {"-db", db, "audit", "missing"}} {
		if err := run(context.Background(), args, &out); err == nil {
			t.Errorf("run(%v) must fail", args)
		}
	}
	if err := run(context.Background(), []string{"-db", db, "-audit-dir", filepath.Join(dir, "no", "such"), "audit", "poison"}, &out); err == nil {
		t.Error("an unwritable audit dir must fail")
	}
}

// TestCompactionKeepsFactInState — п.5: after the history is summarized the
// prompt stops growing, and the fact is still answered right, because it
// lives in user: state and not in the chat.
func TestCompactionKeepsFactInState(t *testing.T) {
	offlineModel(t)
	db := filepath.Join(t.TempDir(), "memory.db")
	q := "Мерчант A-114 питає про повернення коштів за подвійне списання, про терміни виплат і про комісію за міжнародні картки"
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-db", db, "say", "long", "A-114 сидить на legacy-інтеграції."}, &out); err != nil {
		t.Fatal(err)
	}
	var prompts []int
	for i := range 6 {
		out.Reset()
		if err := run(context.Background(), []string{"-db", db, "-threshold", "600", "say", "long", fmt.Sprint(i, ". ", q)}, &out); err != nil {
			t.Fatal(err)
		}
		var p int
		if _, err := fmt.Sscanf(out.String()[strings.Index(out.String(), "prompt="):], "prompt=%d", &p); err != nil {
			t.Fatalf("no prompt tokens in %q", out.String())
		}
		prompts = append(prompts, p)
	}
	if prompts[5] > prompts[3] {
		t.Errorf("prompt still grows with compaction: %v", prompts)
	}
	out.Reset()
	if err := run(context.Background(), []string{"-db", db, "-threshold", "600", "say", "long", "Яка інтеграція в A-114?"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "інтеграція legacy") {
		t.Errorf("fact lost after compaction:\n%s", out.String())
	}
	out.Reset()
	if err := run(context.Background(), []string{"-db", db, "-audit-dir", t.TempDir(), "audit", "long"}, &out); err != nil || !strings.Contains(out.String(), "compaction") {
		t.Errorf("no compaction row in the audit: %v\n%s", err, out.String())
	}
}

func TestPrintUsageWithoutReport(t *testing.T) {
	m := &memlab.Meter{}
	_, _ = m.AfterModel()(nil, &model.LLMResponse{}, nil)
	var out bytes.Buffer
	printUsage(&out, m, false)
	if !strings.Contains(out.String(), "не повернув UsageMetadata") {
		t.Errorf("got %q", out.String())
	}
}
