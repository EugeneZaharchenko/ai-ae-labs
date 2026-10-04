package main

// Тонкий ADK-шар харнесу: метрика, вибір провайдера, побудова агента й прогін
// сценарію. Уся арифметика живе в bench.go — тут лише проводка.
//
// Ключова ідея файлу: щоб виміряти токени й латентність, НЕ треба обгортати
// model.LLM власним типом. ADK дає для цього рівно призначені гачки —
// BeforeModelCallbacks / AfterModelCallbacks, які документація описує як
// «ideal place to log model responses, collect metrics on token usage».
// Обгортка навколо моделі теж працювала б, але вона зламається, щойно агент
// почне ходити через кілька моделей або через workflow-граф; колбеки живуть
// на рівні агента й переживають це.
//
// Перевірено проти google.golang.org/adk/v2 v2.5.0 (станом на 09/2026).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/adkenv"
	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
)

// ErrProviderNotConfigured — для провайдера немає ключа / базового URL.
//
// Це не фатальна помилка харнесу: якщо з трьох моделей налаштована одна,
// правильна поведінка — виміряти одну й сказати про це вголос, а не впасти.
var ErrProviderNotConfigured = errors.New("provider not configured")

// ErrUnsupportedProvider — провайдера немає серед бекендів ADK Go v2.5.0.
var ErrUnsupportedProvider = errors.New("unsupported provider")

// envVar повертає змінну оточення, наявність якої вмикає провайдера.
func envVar(provider string) (string, error) {
	switch strings.ToLower(provider) {
	case "gemini":
		return "GOOGLE_API_KEY", nil
	case "openai":
		return "OPENAI_API_KEY", nil
	case "ollama":
		return "OLLAMA_BASE_URL", nil
	case "agentgateway":
		return "AGENTGATEWAY_BASE_URL", nil
	default:
		// ADK Go v2.5.0 має рівно три модельні пакети: gemini, openaimodel,
		// apigee. Бекенда Anthropic немає — і це обмеження фреймворка, а не
		// недогляд лабораторної.
		return "", fmt.Errorf("%w: %q (ADK Go v2.5.0 ships gemini, openaimodel, apigee)",
			ErrUnsupportedProvider, provider)
	}
}

// Configured повідомляє, чи можна взагалі викликати цю модель у цьому оточенні.
func Configured(spec ModelSpec) bool {
	name, err := envVar(spec.Provider)
	if err != nil {
		return false
	}
	_, ok := adkenv.Key(name)
	return ok
}

// BuildModel створює бекенд для однієї специфікації.
//
// Свідомо коротший за multi-provider блок із Частини 2: там задача була
// «обрати одного провайдера з оточення», тут — «створити конкретного, якого
// назвав каталог». Різні задачі, різні функції.
func BuildModel(ctx context.Context, spec ModelSpec) (model.LLM, error) {
	name, err := envVar(spec.Provider)
	if err != nil {
		return nil, err
	}
	value, ok := adkenv.Key(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s needs %s", ErrProviderNotConfigured, spec.ID, name)
	}

	switch strings.ToLower(spec.Provider) {
	case "gemini":
		m, err := gemini.NewModel(ctx, spec.ID, &genai.ClientConfig{APIKey: value})
		if err != nil {
			return nil, fmt.Errorf("gemini %s: %w", spec.ID, err)
		}
		return m, nil
	case "openai":
		m, err := openaimodel.NewModel(ctx, spec.ID, &openaimodel.ClientConfig{APIKey: value})
		if err != nil {
			return nil, fmt.Errorf("openai %s: %w", spec.ID, err)
		}
		return m, nil
	case "ollama":
		// Локальна модель отримує повноцінний бекенд через BaseURL пакета
		// openaimodel («for OpenAI-compatible endpoints»). Ключ — заглушка:
		// клієнт openai-go відхиляє порожній APIKey ще до запиту, і падіння
		// виглядало б як проблема Ollama, якою воно не є.
		key, _ := adkenv.Key("OLLAMA_API_KEY")
		if key == "" {
			key = "not-needed-for-local-ollama"
		}
		m, err := openaimodel.NewModel(ctx, spec.ID, &openaimodel.ClientConfig{
			APIKey:  key,
			BaseURL: value,
		})
		if err != nil {
			return nil, fmt.Errorf("ollama %s: %w", spec.ID, err)
		}
		return m, nil
	case "agentgateway":
		// Той самий OpenAI-сумісний бекенд, що й для Ollama — змінюється лише
		// BaseURL. Це і є вся «інтеграція» з agentgateway: він виглядає для
		// агента як звичайний OpenAI endpoint на :4000.
		//
		// Навзамін гейтвей дає те, чого агент про себе не знає: трасу в Jaeger,
		// метрики в Prometheus/Grafana і РЕАЛІЗОВАНУ вартість запиту в
		// access-log. Порівняйте з Meter нижче: Meter міряє те, що бачить
		// агент, гейтвей — те, що справді пішло в мережу. Коли ці два числа
		// розходяться, праві зазвичай не ви.
		key, _ := adkenv.Key("AGENTGATEWAY_API_KEY")
		if key == "" {
			key = "not-needed-for-local-agentgateway"
		}
		m, err := openaimodel.NewModel(ctx, spec.ID, &openaimodel.ClientConfig{
			APIKey:  key,
			BaseURL: value,
		})
		if err != nil {
			return nil, fmt.Errorf("agentgateway %s: %w", spec.ID, err)
		}
		return m, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedProvider, spec.Provider)
	}
}

// Meter збирає латентність і токени кожного виклику моделі.
//
// Пара Before/After — це секундомір: Before фіксує старт і текст запиту,
// After рахує тривалість і читає usage-метадані.
//
// Обмеження, про яке варто сказати вголос: пара «старт → фініш» одна на
// Meter, тож коректні виміри він дає для послідовних викликів. Для
// паралельних гілок (Тиждень 6) кожна гілка має отримати власний Meter — інакше
// ви виміряєте суму, вважаючи, що виміряли крок.
type Meter struct {
	tokenizer Tokenizer

	mu      sync.Mutex
	started time.Time
	prompt  string
	samples []Sample
}

// NewMeter створює лічильник із токенайзером конкретної моделі.
func NewMeter(t Tokenizer) *Meter { return &Meter{tokenizer: t} }

// Before запускає секундомір. Повертає (nil, nil), щоб виклик моделі відбувся:
// ненульова відповідь тут означала б «підмінити модель кешем».
func (m *Meter) Before(_ agent.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started = time.Now()
	m.prompt = requestText(req)
	return nil, nil
}

// After зупиняє секундомір і записує вимір.
//
// Помилковий виклик не стає виміром: латентність невдалого запиту — це
// латентність помилки, і змішувати її з успішними прогонами означає
// покращувати статистику моделі за рахунок її ж відмов.
func (m *Meter) After(_ agent.Context, resp *model.LLMResponse, respErr error) (*model.LLMResponse, error) {
	if respErr != nil {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	elapsed := time.Since(m.started)
	usage, estimated := m.usageOf(resp)
	m.samples = append(m.samples, Sample{Latency: elapsed, Usage: usage, Estimated: estimated})
	return nil, nil
}

// usageOf бере токени від провайдера, а якщо їх немає — оцінює сам.
//
// Другий шлях не екзотика: під час стрімінгу й через частину проксі
// usage-метадані не приходять узагалі. Харнес, який у цьому випадку мовчки
// покаже 0 токенів і $0.00, — гірший за відсутній.
func (m *Meter) usageOf(resp *model.LLMResponse) (Usage, bool) {
	if resp != nil && resp.UsageMetadata != nil && resp.UsageMetadata.TotalTokenCount > 0 {
		u := resp.UsageMetadata
		return Usage{
			InputTokens:   int(u.PromptTokenCount),
			OutputTokens:  int(u.CandidatesTokenCount),
			ThoughtTokens: int(u.ThoughtsTokenCount),
		}, false
	}
	return Usage{
		InputTokens:  m.tokenizer.Estimate(m.prompt),
		OutputTokens: m.tokenizer.Estimate(responseText(resp)),
	}, true
}

// Samples повертає копію зібраних вимірів.
func (m *Meter) Samples() []Sample {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Sample, len(m.samples))
	copy(out, m.samples)
	return out
}

// requestText збирає весь текст, який реально пішов у модель — разом із
// системною інструкцією. Саме вона зазвичай і є тим «невидимим» рахунком:
// її платять у КОЖНОМУ виклику, а в промпті користувача її не видно.
func requestText(req *model.LLMRequest) string {
	if req == nil {
		return ""
	}
	var b strings.Builder
	if req.Config != nil && req.Config.SystemInstruction != nil {
		writeParts(&b, req.Config.SystemInstruction)
	}
	for _, c := range req.Contents {
		writeParts(&b, c)
	}
	return b.String()
}

// responseText збирає текст відповіді моделі.
func responseText(resp *model.LLMResponse) string {
	if resp == nil || resp.Content == nil {
		return ""
	}
	var b strings.Builder
	writeParts(&b, resp.Content)
	return b.String()
}

func writeParts(b *strings.Builder, c *genai.Content) {
	if c == nil {
		return
	}
	for _, p := range c.Parts {
		if p != nil && p.Text != "" {
			b.WriteString(p.Text)
			b.WriteString("\n")
		}
	}
}

// benchInstruction — однакова інструкція для всіх бекендів.
//
// Це умова коректності бенчмарку, а не стилю: якщо кожна модель отримує
// «свій, підігнаний» промпт, ви міряєте якість підгонки промпта, а не модель.
// Обмеження довжини теж не косметичне — без нього балакучіша модель програє
// за вартістю просто тому, що вона балакучіша.
const benchInstruction = `You are a benchmark subject for a payment-processing platform team.

Rules:
- Answer in Ukrainian, in at most three sentences.
- Answer only from the information given in the question. Never invent a number,
  a rate, or a policy clause.
- If the question needs data you were not given, say exactly what is missing
  instead of guessing.`

// thinkingLevel мапить рядок каталогу на рівень «роздумів» genai.
//
// Це та сама шоста вісь із лекції, але вже як параметр запиту: вищий рівень —
// більше thought-токенів, вища латентність і вищий рахунок за ту саму задачу.
func thinkingLevel(effort string) genai.ThinkingLevel {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal":
		return genai.ThinkingLevelMinimal
	case "low":
		return genai.ThinkingLevelLow
	case "medium":
		return genai.ThinkingLevelMedium
	case "high":
		return genai.ThinkingLevelHigh
	default:
		return genai.ThinkingLevelUnspecified
	}
}

// NewBenchmarkAgent будує агента-піддослідного разом із його лічильником.
//
// Застереження для продакшену: ThinkingConfig — це поле genai, і його поважає
// бекенд Gemini. Через openaimodel (OpenAI, Ollama) воно може бути просто
// проігнороване — тоді колонка `think tok` чесно покаже нулі. Це нормально й
// це варто сказати вголос: «однаковий конфіг» і «однакова поведінка» — різні
// речі, коли під капотом різні діалекти API.
func NewBenchmarkAgent(spec ModelSpec, m model.LLM) (agent.Agent, *Meter, error) {
	if m == nil {
		return nil, nil, fmt.Errorf("model is nil for %q", spec.ID)
	}
	meter := NewMeter(spec.Tokenizer)

	cfg := llmagent.Config{
		Name:                 "benchmark_subject",
		Model:                m,
		Description:          "Runs one fixed scenario so that models can be compared on equal terms.",
		Instruction:          benchInstruction,
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{meter.Before},
		AfterModelCallbacks:  []llmagent.AfterModelCallback{meter.After},
	}
	if level := thinkingLevel(spec.ReasoningEffort); level != genai.ThinkingLevelUnspecified {
		cfg.GenerateContentConfig = &genai.GenerateContentConfig{
			ThinkingConfig: &genai.ThinkingConfig{ThinkingLevel: level},
		}
	}

	a, err := llmagent.New(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("build benchmark agent for %q: %w", spec.ID, err)
	}
	return a, meter, nil
}

// RunScenario проганяє сценарій n разів і повертає зібрані виміри.
//
// Дві деталі, які роблять числа порівнюваними:
//
//   - перевірка контексту робиться ДО першого запиту: інакше ви заплатите за
//     вхідні токени, щоб дізнатися, що запит завеликий;
//   - кожен прогін іде у власній сесії (labrun.Run), тож другий вимір не
//     тягне за собою історію першого. Без цього кожен наступний прогін
//     дорожчий за попередній, і ви виміряєте зростання контексту, а не модель.
func RunScenario(ctx context.Context, spec ModelSpec, m model.LLM, sc Scenario, n int) (Result, error) {
	if n <= 0 {
		return Result{}, fmt.Errorf("%w: need at least one run of %q", ErrNoSamples, sc.Name)
	}
	if err := spec.FitsContext(sc); err != nil {
		return Result{}, err
	}

	a, meter, err := NewBenchmarkAgent(spec, m)
	if err != nil {
		return Result{}, err
	}
	for i := range n {
		if _, err := labrun.Run(ctx, a, sc.Prompt); err != nil {
			return Result{}, fmt.Errorf("%s run %d/%d: %w", spec.ID, i+1, n, err)
		}
	}
	return Result{Spec: spec, Scenario: sc.Name, Samples: meter.Samples()}, nil
}

// LoadEnv підтягує apps/.env, мовчки переживаючи його відсутність — офлайн-шлях
// має працювати без жодного ключа.
func LoadEnv() error {
	if err := adkenv.Load("."); err != nil && !errors.Is(err, adkenv.ErrNotFound) {
		return err
	}
	return nil
}
