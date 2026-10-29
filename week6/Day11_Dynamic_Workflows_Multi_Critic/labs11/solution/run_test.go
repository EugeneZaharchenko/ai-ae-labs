package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
	"iter"

	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/billing"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/gate"
)

// offlineResolver імітує «жодного провайдера не налаштовано».
func offlineResolver() Resolver {
	return Resolver{
		Lookup: func(string) (string, bool) { return "", false },
		Default: func(context.Context) (model.LLM, modelcfg.Choice, error) {
			return nil, modelcfg.Choice{}, errors.New("жодного провайдера")
		},
		Build: func(context.Context, string, string) (model.LLM, error) {
			return nil, errors.New("не має викликатись")
		},
	}
}

func TestRun_OfflineSingleTask(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-task", "prorate_refund"}, &out, offlineResolver()); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := out.String()
	for _, want := range []string{"ОФЛАЙН-РЕЖИМ", "жодного провайдера", "прийнято: true", "holdout: так", "токени"} {
		if !strings.Contains(got, want) {
			t.Errorf("у виводі немає %q:\n%s", want, got)
		}
	}
}

func TestRun_UnknownTask(t *testing.T) {
	err := run(context.Background(), []string{"-task", "nope"}, &bytes.Buffer{}, offlineResolver())
	if err == nil || !strings.Contains(err.Error(), "невідоме завдання") {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_BadFlags(t *testing.T) {
	if err := run(context.Background(), []string{"-write-baseline"}, &bytes.Buffer{}, offlineResolver()); err == nil {
		t.Fatal("-write-baseline без -baseline має бути помилкою")
	}
}

func TestRun_EvalWithBaseline(t *testing.T) {
	base := filepath.Join(t.TempDir(), "base.json")
	var out bytes.Buffer
	args := []string{"-eval", "-offline", "-baseline", base, "-price-in", "1", "-price-out", "2"}

	if err := run(context.Background(), append(args, "-write-baseline"), &out, offlineResolver()); err != nil {
		t.Fatalf("write-baseline: %v", err)
	}
	for _, want := range []string{"| задача |", "вартість, $", "базу записано", "прийнято циклом, але хибне на holdout: 0"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("у виводі немає %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if err := run(context.Background(), args, &out, offlineResolver()); err != nil {
		t.Fatalf("порівняння з базою: %v", err)
	}
	if !strings.Contains(out.String(), "регресій відносно бази немає") {
		t.Errorf("очікував чисте порівняння:\n%s", out.String())
	}
}

func TestBaselineStep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.json")
	good := Suite{Outcomes: []Outcome{{TaskID: "a", HoldoutOK: true}}}
	bad := Suite{Outcomes: []Outcome{{TaskID: "a", HoldoutOK: false}}}

	if err := baselineStep(&bytes.Buffer{}, config{}, good); err != nil {
		t.Fatalf("без -baseline нічого не робимо: %v", err)
	}
	err := baselineStep(&bytes.Buffer{}, config{baseline: path}, good)
	if !errors.Is(err, ErrNoBaseline) {
		t.Fatalf("відсутня база: %v", err)
	}
	if err := baselineStep(&bytes.Buffer{}, config{baseline: path, writeBase: true}, good); err != nil {
		t.Fatal(err)
	}
	err = baselineStep(&bytes.Buffer{}, config{baseline: path}, bad)
	if err == nil || !strings.Contains(err.Error(), "регресія") {
		t.Fatalf("очікував регресію, отримав %v", err)
	}
}

func TestLoadBaseline_Errors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.json")
	if err := os.WriteFile(p, []byte("{не json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBaseline(p); err == nil || errors.Is(err, ErrNoBaseline) {
		t.Fatalf("зламаний JSON: %v", err)
	}
	if err := SaveBaseline(filepath.Join(t.TempDir(), "no", "dir.json"), Suite{}); err == nil {
		t.Fatal("запис у неіснуючий каталог має падати")
	}
}

func TestRegressions(t *testing.T) {
	base := Suite{Outcomes: []Outcome{{TaskID: "a", HoldoutOK: true}, {TaskID: "b", HoldoutOK: false}}}
	cur := Suite{Outcomes: []Outcome{{TaskID: "a", HoldoutOK: false}, {TaskID: "b", HoldoutOK: false}, {TaskID: "new"}}}
	got := Regressions(base, cur)
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("Regressions = %v, хочемо [a]", got)
	}
}

func TestSuiteMarkdown(t *testing.T) {
	s := Suite{Outcomes: []Outcome{
		{TaskID: "a", Accepted: true, HoldoutOK: true},
		{TaskID: "b", Accepted: true, HoldoutStage: "test"},
		{TaskID: "c"},
	}}
	md := s.Markdown(nil)
	for _, want := range []string{"| a | так |", "ні (test)", "| c | ні |", "хибне на holdout: 1", "holdout пройдено: 1 з 3"} {
		if !strings.Contains(md, want) {
			t.Errorf("немає %q:\n%s", want, md)
		}
	}
}

func TestNum(t *testing.T) {
	if num(3) != 3 || num(3.0) != 3 || num("x") != 0 {
		t.Fatal("num")
	}
}

func TestRunTask_BadConfig(t *testing.T) {
	r := RunTask(context.Background(), billing.Tasks()[0], Models{}, Options{MaxAttempts: 2})
	if r.Outcome.Err == "" || r.Outcome.Accepted {
		t.Fatalf("без моделей агент не збирається: %+v", r.Outcome)
	}
}

func TestRunSuite_Offline(t *testing.T) {
	task, _ := billing.ByID("prorate_refund")
	s := RunSuite(context.Background(), []billing.Task{task}, OfflineModels(task), Options{MaxAttempts: 4, Gate: gate.Runner{}})
	if len(s.Outcomes) != 1 || !s.Outcomes[0].HoldoutOK || s.Models["maker"] == "" {
		t.Fatalf("suite = %+v", s)
	}
}

func TestExecGrader_GreenAndRed(t *testing.T) {
	task, _ := billing.ByID("prorate_refund")
	g := ExecGrader{Test: task.Visible}
	if rep := g.Grade(context.Background(), task.Reference); !rep.OK {
		t.Fatalf("еталон має бути зеленим: %+v", rep)
	}
	rep := g.Grade(context.Background(), stubCode)
	if rep.OK || len(rep.Failures) == 0 || rep.Stage == "" {
		t.Fatalf("заглушка має падати з причиною: %+v", rep)
	}
}

func TestLoopResultReport(t *testing.T) {
	r := LoopResult{Accepted: true, Attempts: []Attempt{{N: 1, Grade: GradeReport{Stage: "build"}}}}
	if rep := r.Report(); !strings.Contains(rep, "#1") || !strings.Contains(rep, "прийнято: true") {
		t.Fatalf("report = %q", rep)
	}
}

// ── вибір моделей ───────────────────────────────────────────────────────────

func TestResolve_DefaultSharedAndWarns(t *testing.T) {
	task, _ := billing.ByID("validate_iban")
	stub := OfflineModels(task).Maker
	builds := 0
	r := Resolver{
		Lookup: func(string) (string, bool) { return "", false },
		Default: func(context.Context) (model.LLM, modelcfg.Choice, error) {
			builds++
			return stub, modelcfg.Choice{Provider: "ollama", Model: "m1", Reason: "тест"}, nil
		},
		Build: func(context.Context, string, string) (model.LLM, error) { return stub, nil },
	}
	m, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Errorf("модель за замовчуванням будується раз, а було %d", builds)
	}
	if len(m.Warnings) != 3 {
		t.Errorf("усі троє критиків на моделі maker'а → 3 попередження, є %d", len(m.Warnings))
	}
	d := m.Describe()
	if !strings.Contains(d, "ollama / m1") || !strings.Contains(d, "УВАГА") {
		t.Errorf("Describe:\n%s", d)
	}
}

func TestResolve_PerRoleOverride(t *testing.T) {
	task, _ := billing.ByID("validate_iban")
	stub := OfflineModels(task).Maker
	r := Resolver{
		Lookup: func(name string) (string, bool) {
			if name == "LAB11_STYLE_MODEL" {
				return "ollama/small", true
			}
			return "", false
		},
		Default: func(context.Context) (model.LLM, modelcfg.Choice, error) {
			return stub, modelcfg.Choice{Provider: "ollama", Model: "big"}, nil
		},
		Build: func(_ context.Context, p, n string) (model.LLM, error) { return stub, nil },
	}
	m, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Choices["style"].Model; !strings.HasSuffix(got, "small") {
		t.Errorf("style model = %q", got)
	}
	if len(m.Warnings) != 2 {
		t.Errorf("попереджень має бути 2 (correctness, performance), є %v", m.Warnings)
	}
}

func TestResolve_Errors(t *testing.T) {
	task, _ := billing.ByID("validate_iban")
	stub := OfflineModels(task).Maker
	okDefault := func(context.Context) (model.LLM, modelcfg.Choice, error) {
		return stub, modelcfg.Choice{Provider: "ollama", Model: "m"}, nil
	}
	if _, err := offlineResolver().Resolve(context.Background()); err == nil {
		t.Error("без провайдера Resolve має повертати помилку")
	}
	badChoice := Resolver{
		Lookup:  func(n string) (string, bool) { return "claude-without-route", n == "LAB11_MAKER_MODEL" },
		Default: okDefault,
		Build:   func(context.Context, string, string) (model.LLM, error) { return stub, nil },
	}
	if _, err := badChoice.Resolve(context.Background()); err == nil || !strings.Contains(err.Error(), "LAB11_MAKER_MODEL") {
		t.Errorf("невалідний MODEL ролі має називати змінну: %v", err)
	}

	buildFails := Resolver{
		Lookup:  func(n string) (string, bool) { return "ollama/x", n == "LAB11_CORRECTNESS_MODEL" },
		Default: okDefault,
		Build: func(context.Context, string, string) (model.LLM, error) {
			return nil, errors.New("немає ключа")
		},
	}
	if _, err := buildFails.Resolve(context.Background()); err == nil || !strings.Contains(err.Error(), "LAB11_CORRECTNESS_MODEL") {
		t.Errorf("помилка має називати змінну: %v", err)
	}
}

func TestDefaultResolver_Wired(t *testing.T) {
	r := DefaultResolver()
	if r.Lookup == nil || r.Default == nil || r.Build == nil {
		t.Fatal("DefaultResolver має заповнити всі залежності")
	}
}

func TestParseFlagsDefaults(t *testing.T) {
	c, err := parseFlags(nil, &bytes.Buffer{})
	if err != nil || c.task != "prorate_refund" || c.maxAttempts != DefaultMaxAttempts {
		t.Fatalf("%+v %v", c, err)
	}
	if !strings.Contains(taskIDs(), "validate_iban") || firstLine(errors.New("a\nb")) != "a" {
		t.Fatal("taskIDs/firstLine")
	}
}

func TestUnfenceVerdict(t *testing.T) {
	fenced := &model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{Text: "```json\n{\"passed\":true}\n```"}}}}
	got, err := unfenceVerdict(nil, fenced, nil)
	if err != nil || got == nil || got.Content.Parts[0].Text != `{"passed":true}` {
		t.Fatalf("фенс не знято: %+v %v", got, err)
	}
	plain := &model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{Text: `{"passed":true}`}}}}
	if got, _ := unfenceVerdict(nil, plain, nil); got != nil {
		t.Error("чистий JSON не чіпаємо")
	}
	for _, r := range []*model.LLMResponse{nil, {Partial: true}, {}} {
		if got, _ := unfenceVerdict(nil, r, nil); got != nil {
			t.Error("порожні/часткові відповіді ігноруємо")
		}
	}
	if got, _ := unfenceVerdict(nil, fenced, errors.New("x")); got != nil {
		t.Error("на помилці не чіпаємо")
	}
}

func TestExtractVerdictJSON(t *testing.T) {
	v := `{"critic":"style","passed":true}`
	for name, in := range map[string]string{
		"чистий":      v,
		"фенс":        "```json\n" + v + "\n```",
		"проза до":    "Let me think {about} it.\n" + v,
		"проза після": v + "\nHope this helps!",
	} {
		if got := extractVerdictJSON(in); got != v {
			t.Errorf("%s: %q", name, got)
		}
	}
	if got := extractVerdictJSON("лише проза"); got != "лише проза" {
		t.Errorf("без JSON текст лишається як є: %q", got)
	}
}

type errModel struct{ model.LLM }

func (errModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) { yield(nil, errors.New("410 Gone")) }
}

func TestSoftFail_ErrorBecomesRejectedVerdict(t *testing.T) {
	var got *model.LLMResponse
	for r, err := range (softFail{errModel{}}).GenerateContent(context.Background(), &model.LLMRequest{}, false) {
		if err != nil {
			t.Fatalf("softFail не має віддавати помилку: %v", err)
		}
		got = r
	}
	if got == nil || !strings.Contains(got.Content.Parts[0].Text, "410 Gone") {
		t.Fatalf("причина має дійти до вердикту: %+v", got)
	}
	if _, err := ParseVerdict(got.Content.Parts[0].Text, RoleStyle); err == nil {
		t.Error("відповідь-заглушка не має проходити як вердикт")
	}
}
