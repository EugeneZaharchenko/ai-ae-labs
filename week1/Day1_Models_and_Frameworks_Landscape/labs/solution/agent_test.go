package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
)

// testSpec — специфікація, достатня, щоб прогнати сценарій без провайдера.
func testSpec() ModelSpec {
	return ModelSpec{
		ID:              "fake-model",
		Provider:        "gemini",
		Label:           "Fake",
		ContextTokens:   100_000,
		ReasoningEffort: "medium",
		Tokenizer:       Tokenizer{Name: "fake", CharsPerToken: 4},
		Pricing:         Pricing{InputPerMTok: 1, OutputPerMTok: 10},
		AsOf:            "08/2026",
	}
}

func testScenario() Scenario {
	return Scenario{
		Name:                 "merchant-fee-question",
		Prompt:               "Яка комісія за chargeback на тарифі мерчанта A-114?",
		ExpectedOutputTokens: 128,
	}
}

// TestRunScenarioMeasuresEveryRun — наскрізний тест харнесу: агент реально
// проганяється через runner, а колбеки Before/After дають по одному виміру на
// виклик моделі. Без ключа, без мережі, детерміновано.
func TestRunScenarioMeasuresEveryRun(t *testing.T) {
	const runs = 3
	turns := make([]fakellm.Turn, 0, runs)
	for range runs {
		turns = append(turns, fakellm.TextTurn("Тарифну сітку мені не передавали, тому назвати комісію не можу."))
	}
	m := fakellm.New("fake", turns...)

	res, err := RunScenario(context.Background(), testSpec(), m, testScenario(), runs)
	if err != nil {
		t.Fatalf("RunScenario() error = %v", err)
	}

	if res.N() != runs {
		t.Fatalf("зібрано %d вимірів, want %d", res.N(), runs)
	}
	if m.CallCount() != runs {
		t.Errorf("CallCount() = %d, want %d", m.CallCount(), runs)
	}
	if m.Remaining() != 0 {
		t.Errorf("Remaining() = %d; агент зупинився раніше, ніж очікував тест", m.Remaining())
	}
	if res.Scenario != testScenario().Name {
		t.Errorf("Scenario = %q, want %q", res.Scenario, testScenario().Name)
	}

	for i, s := range res.Samples {
		if s.Latency <= 0 {
			t.Errorf("вимір %d: Latency = %v, want > 0", i, s.Latency)
		}
		// Скриптована модель не повертає usage-метаданих — так само, як
		// стрімінг у реальних провайдерів. Харнес мусить оцінити токени сам
		// і чесно позначити це.
		if !s.Estimated {
			t.Errorf("вимір %d: Estimated = false, хоча fakellm не віддає usage", i)
		}
		if s.Usage.InputTokens <= 0 {
			t.Errorf("вимір %d: InputTokens = %d; системну інструкцію теж треба рахувати",
				i, s.Usage.InputTokens)
		}
		if s.Usage.OutputTokens <= 0 {
			t.Errorf("вимір %d: OutputTokens = %d", i, s.Usage.OutputTokens)
		}
	}

	cost, err := res.CostPerTask()
	if err != nil {
		t.Fatalf("CostPerTask() error = %v", err)
	}
	if cost <= 0 {
		t.Errorf("CostPerTask() = %v, want > 0", cost)
	}
}

// TestBenchmarkAgentSendsSameInstructionToEveryModel — умова коректності
// порівняння: різні бекенди отримують той самий системний промпт. Якщо кожна
// модель дістає «свій, підігнаний» текст, таблиця міряє якість підгонки, а не
// моделі.
func TestBenchmarkAgentSendsSameInstructionToEveryModel(t *testing.T) {
	sc := testScenario()

	sent := func(spec ModelSpec) string {
		m := fakellm.New(spec.ID, fakellm.TextTurn("ок"))
		if _, err := RunScenario(context.Background(), spec, m, sc, 1); err != nil {
			t.Fatalf("RunScenario(%s) error = %v", spec.ID, err)
		}
		reqs := m.Requests()
		if len(reqs) != 1 {
			t.Fatalf("запитів до моделі = %d, want 1", len(reqs))
		}
		return requestText(reqs[0])
	}

	specA := testSpec()
	specB := testSpec()
	specB.ID = "other-model"
	specB.ReasoningEffort = "high"

	textA, textB := sent(specA), sent(specB)
	if textA != textB {
		t.Errorf("моделі отримали різний вхід:\nA: %q\nB: %q", textA, textB)
	}
	if !strings.Contains(textA, "at most three sentences") {
		t.Errorf("системна інструкція не дійшла до моделі; надіслано: %q", textA)
	}
	if !strings.Contains(textA, "chargeback") {
		t.Errorf("промпт сценарію не дійшов до моделі; надіслано: %q", textA)
	}
}

// meterCtx — строгий agent.Context. Будь-який метод, якого ми не очікували,
// панікує замість того, щоб тихо повернути нульове значення.
type meterCtx struct {
	agent.StrictContextMock
}

func newMeterCtx() agent.Context {
	return &meterCtx{StrictContextMock: agent.NewStrictContextMock(context.Background())}
}

// TestMeterPrefersProviderUsage — коли провайдер повертає usage, беремо його,
// а не власну оцінку: рахунок виставляють за їхніми токенами, не за нашими.
func TestMeterPrefersProviderUsage(t *testing.T) {
	meter := NewMeter(Tokenizer{CharsPerToken: 4})
	ctx := newMeterCtx()

	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("питання", genai.RoleUser)},
	}
	if _, err := meter.Before(ctx, req); err != nil {
		t.Fatalf("Before() error = %v", err)
	}
	time.Sleep(time.Millisecond)

	resp := &model.LLMResponse{
		Content: genai.NewContentFromText("відповідь", "model"),
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     1200,
			CandidatesTokenCount: 400,
			ThoughtsTokenCount:   250,
			TotalTokenCount:      1850,
		},
	}
	if _, err := meter.After(ctx, resp, nil); err != nil {
		t.Fatalf("After() error = %v", err)
	}

	samples := meter.Samples()
	if len(samples) != 1 {
		t.Fatalf("вимірів = %d, want 1", len(samples))
	}
	got := samples[0]
	if got.Estimated {
		t.Error("Estimated = true, хоча провайдер повернув usage")
	}
	want := Usage{InputTokens: 1200, OutputTokens: 400, ThoughtTokens: 250}
	if got.Usage != want {
		t.Errorf("Usage = %+v, want %+v", got.Usage, want)
	}
	if got.Usage.BilledOutput() != 650 {
		t.Errorf("BilledOutput() = %d, want 650 (thought-токени оплачуються як вихідні)",
			got.Usage.BilledOutput())
	}
	if got.Latency <= 0 {
		t.Errorf("Latency = %v, want > 0", got.Latency)
	}
}

// TestMeterSkipsFailedCalls — невдалий виклик не стає виміром. Інакше таймаут
// провайдера потрапив би в медіану як «швидка відповідь» або навпаки
// зіпсував би розкид, і модель отримала б статистику за свої ж відмови.
func TestMeterSkipsFailedCalls(t *testing.T) {
	meter := NewMeter(Tokenizer{CharsPerToken: 4})
	ctx := newMeterCtx()

	if _, err := meter.Before(ctx, &model.LLMRequest{}); err != nil {
		t.Fatalf("Before() error = %v", err)
	}
	resp, err := meter.After(ctx, nil, errors.New("429 rate limit"))
	if err != nil {
		t.Fatalf("After() error = %v; колбек не має підміняти помилку моделі", err)
	}
	if resp != nil {
		t.Error("After() повернув відповідь; це підмінило б реальну помилку моделі")
	}
	if n := len(meter.Samples()); n != 0 {
		t.Errorf("вимірів = %d, want 0", n)
	}
}

// TestRunScenarioRefusesToPayForAnOverflow — негативний тест із грошима.
//
// Сценарій, що не влазить у контекст, має відсіятись ДО виклику моделі:
// провайдер відхилить завеликий запит уже після того, як порахує вхідні
// токени. Перевіряємо не лише помилку, а й те, що моделі ніхто не потурбував.
func TestRunScenarioRefusesToPayForAnOverflow(t *testing.T) {
	spec := testSpec()
	spec.ContextTokens = 50

	sc := testScenario()
	sc.Prompt = strings.Repeat("довгий фрагмент договору. ", 200)

	m := fakellm.New("fake", fakellm.TextTurn("не має бути викликано"))

	_, err := RunScenario(context.Background(), spec, m, sc, 1)
	if !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("RunScenario() error = %v, want ErrContextOverflow", err)
	}
	if m.CallCount() != 0 {
		t.Errorf("CallCount() = %d; за завеликий запит уже заплачено", m.CallCount())
	}
}

func TestRunScenarioRejectsZeroRuns(t *testing.T) {
	m := fakellm.New("fake", fakellm.TextTurn("ок"))
	if _, err := RunScenario(context.Background(), testSpec(), m, testScenario(), 0); !errors.Is(err, ErrNoSamples) {
		t.Errorf("RunScenario(n=0) error = %v, want ErrNoSamples", err)
	}
}

// --- Вибір бекенда ----------------------------------------------------------

func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GOOGLE_API_KEY", "OPENAI_API_KEY", "OLLAMA_BASE_URL", "OLLAMA_API_KEY", "AGENTGATEWAY_BASE_URL", "AGENTGATEWAY_API_KEY"} {
		t.Setenv(k, "")
	}
}

func TestConfiguredFollowsEnvironment(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		env      map[string]string
		want     bool
	}{
		{"gemini без ключа", "gemini", nil, false},
		{"gemini з ключем", "gemini", map[string]string{"GOOGLE_API_KEY": "g"}, true},
		{"openai з ключем", "openai", map[string]string{"OPENAI_API_KEY": "o"}, true},
		{"ollama з base url", "ollama", map[string]string{"OLLAMA_BASE_URL": "http://localhost:11434/v1"}, true},
		{"agentgateway з base url", "agentgateway", map[string]string{"AGENTGATEWAY_BASE_URL": "http://localhost:4000/v1"}, true},
		{"agentgateway без base url — ключ не рятує", "agentgateway", map[string]string{"AGENTGATEWAY_API_KEY": "k"}, false},
		{"порожній ключ — це не ключ", "gemini", map[string]string{"GOOGLE_API_KEY": ""}, false},
		{"невідомий провайдер", "anthropic", map[string]string{"ANTHROPIC_API_KEY": "a"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearProviderEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			spec := testSpec()
			spec.Provider = tc.provider
			if got := Configured(spec); got != tc.want {
				t.Errorf("Configured(%s) = %v, want %v", tc.provider, got, tc.want)
			}
		})
	}
}

// TestBuildModelFailsLoudly — обидві відмови мають бути читабельними: у
// ADK Go v2.5.0 немає бекенда Anthropic, і немає магії «якось запуститись»
// без ключа.
func TestBuildModelFailsLoudly(t *testing.T) {
	clearProviderEnv(t)

	spec := testSpec()
	if _, err := BuildModel(context.Background(), spec); !errors.Is(err, ErrProviderNotConfigured) {
		t.Errorf("BuildModel() без ключа error = %v, want ErrProviderNotConfigured", err)
	}

	spec.Provider = "anthropic"
	_, err := BuildModel(context.Background(), spec)
	if !errors.Is(err, ErrUnsupportedProvider) {
		t.Fatalf("BuildModel(anthropic) error = %v, want ErrUnsupportedProvider", err)
	}
	if !strings.Contains(err.Error(), "gemini") {
		t.Errorf("повідомлення %q не підказує, які бекенди існують", err)
	}
}

func TestThinkingLevel(t *testing.T) {
	tests := []struct {
		effort string
		want   genai.ThinkingLevel
	}{
		{"minimal", genai.ThinkingLevelMinimal},
		{"low", genai.ThinkingLevelLow},
		{"MEDIUM", genai.ThinkingLevelMedium},
		{" high ", genai.ThinkingLevelHigh},
		{"", genai.ThinkingLevelUnspecified},
		{"turbo", genai.ThinkingLevelUnspecified},
	}
	for _, tc := range tests {
		t.Run(tc.effort, func(t *testing.T) {
			if got := thinkingLevel(tc.effort); got != tc.want {
				t.Errorf("thinkingLevel(%q) = %q, want %q", tc.effort, got, tc.want)
			}
		})
	}
}
