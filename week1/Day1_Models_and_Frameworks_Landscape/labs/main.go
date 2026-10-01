// Стартовий шаблон ДЗ 1 — звірено з google.golang.org/adk/v2 v2.5.0 (Go 1.27),
// станом на 08/2026. Перевірте актуальність API перед записом.
//
// Запуск:
//
//	go run . console
//
// Ключ можна не експортувати: якщо в корені репозиторію є apps/.env (шаблон —
// apps/.env-example), він підхопиться сам. Явний export завжди має приоритет.
// Без аргументів launcher за замовчуванням бере console (перший sublauncher
// у full.NewLauncher), а console читає os.Stdin — тому запит можна подати
// пайпом, без інтерактиву. Зручно для міні-бенчмарку з Завдання 5:
//
//	echo "Яка погода у Львові?" | MODEL=gpt-5.6-luna go run .
//
// Провайдера і модель можна не задавати взагалі: якщо в apps/.env (або в
// оточенні) є ключ провайдера, стартер визначить його сам. Провайдера задає
// DEFAULT_MODEL_PROVIDER, модель — MODEL. MODEL — це ім'я моделі ЦІЛКОМ,
// разом із префіксом маршруту:
//
//	DEFAULT_MODEL_PROVIDER=agentgateway/ollama go run . console
//	MODEL=agentgateway/openai/gpt-5.6-luna go run . console
//
// Уся логіка вибору провайдера живе в provider.go — цей файл лише збирає
// застосунок: оточення, бекенд, агент, launcher.
//
// Основано на sources/adk-go/examples/quickstart/main.go
package main

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log"
	"os"
	"time"

	"github.com/dimetron/ai-eng-course/labs/internal/adkenv"
	"google.golang.org/adk/v2/model"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/cmd/launcher/full"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/geminitool"
)

// loadEnv підтягує apps/.env (шукає вгору від робочої теки), мовчки переживаючи
// його відсутність — офлайн-шлях має працювати й без жодного ключа.
//
// Явний export GOOGLE_API_KEY=... завжди виграє в файлу: adkenv.Load заповнює
// лише ті змінні, яких ще немає в оточенні. Тому CI та одноразовий прогін
// з іншим ключем не потребують правки .env.
//
// Порядок обов'язковий: це bootstrap оточення застосунку, і викликати його
// треба ДО LoadModel — інакше ключі з файлу ще не в оточенні, і автовизначення
// провайдера їх не побачить.
func loadEnv() {
	if err := adkenv.Load("."); err != nil && !errors.Is(err, adkenv.ErrNotFound) {
		fmt.Fprintf(os.Stderr, "попередження: %v\n", err)
	}
}

func main() {
	ctx := context.Background()

	loadEnv()

	// TODO(студент): для міні-бенчмарку запустіть агента по черзі на 2–3 моделях
	// («станом на 08/2026»: gemini-3.8-flash, gpt-5.6-luna, deepseek-v4.1-flash:cloud)
	// і зафіксуйте latency/якість/приблизну вартість у таблиці в README.
	// Поруч додайте фундаментальну карту: LLM, prompt, context window, tool,
	// agent, RAG і MCP — що означає кожен термін саме в цій лабі.
	//
	// Модель береться зі змінної MODEL саме для цього: прогнати той самий
	// запит на кількох моделях має бути зміною однієї змінної, а не правкою
	// коду — інакше ви порівнюєте дві різні програми, а не дві моделі.
	// Провайдера задає DEFAULT_MODEL_PROVIDER, а якщо його немає — префікс
	// у самому MODEL, напр. MODEL=agentgateway/openai/gpt-5.6-luna.

	m, choice, err := LoadModel(ctx)
	if err != nil {
		log.Fatalf("%v", err)
	}
	log.Printf("Модель: %s", choice.Reason)

	// in main:
	m = timedLLM{m} // then pass to llmagent.Config.Model

	a, err := llmagent.New(llmagent.Config{
		Name:        "weekend_planner",
		Model:       m,
		Description: "Агент-планувальник вихідних у моєму місті.",
		// TODO(студент): напишіть власну інструкцію:
		//  1) ваше місто та формат відповіді (ранок/день/вечір, бюджет);
		//  2) агент МУСИТЬ використовувати пошук для актуальних подій/погоди;
		//  3) агент МУСИТЬ відмовлятися відповідати поза своїм доменом.
		//  4) у README перевірте context-stress сценарій: коли інформація є
		//     в prompt, а коли агент має чесно сказати, що потрібен пошук/RAG.
		Instruction: "Ти — планувальник вихідних. Відповідайте, пише українською",
		Tools: []tool.Tool{
			geminitool.GoogleSearch{},
		},
	})
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	config := &launcher.Config{
		AgentLoader: agent.NewSingleLoader(a),
	}

	l := full.NewLauncher()
	if err = l.Execute(ctx, config, os.Args[1:]); err != nil {
		log.Fatalf("Run failed: %v\n\n%s", err, l.CommandLineSyntax())
	}
}

type timedLLM struct{ model.LLM }

func (t timedLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		start := time.Now()
		first := time.Duration(0)
		for resp, err := range t.LLM.GenerateContent(ctx, req, stream) {
			if first == 0 {
				first = time.Since(start)
			} // TTFB
			if !yield(resp, err) {
				return
			}
		}
		log.Printf("[llm call] model=%s ttfb=%s total=%s stream=%t\n", req.Model, first, time.Since(start), stream)
	}
}
