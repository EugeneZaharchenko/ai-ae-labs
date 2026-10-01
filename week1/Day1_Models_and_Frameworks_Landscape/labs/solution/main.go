// Command benchmark — Cross-Model Benchmark Harness, Тиждень 1 Частина 1.
//
// Той самий сценарій проганяється на кількох моделях, і на виході — таблиця
// «модель × латентність × токени × вартість задачі», а не думка про те, хто
// кращий.
//
// Запуск:
//
//	go run .            # офлайн: працює БЕЗ жодного ключа й без мережі
//	go run . -live      # реальні провайдери з apps/.env (пропускає неналаштовані)
//	go run . -live -n 5 # п'ять прогонів на модель замість трьох
//
// Офлайн-режим підставляє замість провайдера скриптовану модель. Латентність
// у ньому не означає нічого (запиту в мережу немає), а от токени, токенайзери
// й арифметика вартості — цілком справжні: саме на них видно, чому нижча ціна
// за токен не дорівнює нижчому рахунку за задачу.
//
// Перевірено проти google.golang.org/adk/v2 v2.5.0 (станом на 09/2026).
package main

import (
	"bytes"
	"context"
	_ "embed"
	"flag"
	"fmt"
	"log"
	"os"

	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
)

// catalogJSON вшитий у бінарник, щоб `go run` працював із будь-якої теки.
// Файл поруч із кодом — його можна оновити без перекомпіляції логіки, і саме
// туди йде щоквартальна звірка цін.
//
//go:embed catalog.json
var catalogJSON []byte

// scenario — одне завдання для всіх моделей. Домен LEDGERWORKS: питання про
// комісію, на яке чесна відповідь — «даних немає», бо тарифи в контекст не
// передані. Це навмисно: так у таблиці видно і вартість, і поведінку на межі
// домену.
var scenario = Scenario{
	Name: "merchant-fee-question",
	Prompt: "Мерчант A-114 питає, яка комісія за chargeback на його тарифі. " +
		"Тарифну сітку тобі не передавали. Відповідай за правилами.",
	ExpectedOutputTokens: 256,
}

// offlineAnswer — відповідь скриптованої моделі. Довжина взята близькою до
// реальної трисентенційної відповіді, бо на десяти токенах різниця
// токенайзерів не видно, а саме вона тут і викладається.
const offlineAnswer = "Наразі я не можу назвати комісію за chargeback для мерчанта A-114: " +
	"тарифна сітка не була передана мені в цьому запиті, а називати ставку з пам'яті " +
	"означало б вигадати число там, де рахуються гроші. Щоб відповісти точно, потрібен " +
	"актуальний тарифний план мерчанта або доступ до довідника тарифів. " +
	"Передайте відповідний фрагмент договору — і я порахую комісію за ним."

func main() {
	live := flag.Bool("live", false, "прогнати справжніх провайдерів замість скриптованої моделі")
	n := flag.Int("n", 3, "кількість прогонів на модель")
	flag.Parse()

	if err := run(context.Background(), *live, *n); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, live bool, n int) error {
	catalog, err := LoadCatalog(bytes.NewReader(catalogJSON))
	if err != nil {
		return err
	}
	if live {
		if err := LoadEnv(); err != nil {
			fmt.Fprintf(os.Stderr, "попередження: %v\n", err)
		}
	}

	results := make([]Result, 0, len(catalog.Models))
	for _, spec := range catalog.Models {
		m, err := modelFor(ctx, spec, live)
		if err != nil {
			// Ненастроєний провайдер — не привід валити весь бенчмарк.
			// Пропускаємо його ВГОЛОС: мовчазний пропуск дав би таблицю
			// на дві моделі, яку прочитали б як таблицю на три.
			fmt.Fprintf(os.Stderr, "пропускаємо %s: %v\n", spec.ID, err)
			continue
		}
		res, err := RunScenario(ctx, spec, m, scenario, n)
		if err != nil {
			fmt.Fprintf(os.Stderr, "пропускаємо %s: %v\n", spec.ID, err)
			continue
		}
		results = append(results, res)
	}

	report, err := Compare(results)
	if err != nil {
		return err
	}

	fmt.Print(report.Markdown())
	fmt.Print("\n", footer(live, catalog))
	return nil
}

// modelFor віддає або справжній бекенд, або скриптовану модель.
func modelFor(ctx context.Context, spec ModelSpec, live bool) (model.LLM, error) {
	if !live {
		turns := make([]fakellm.Turn, 0, 8)
		for range 8 {
			turns = append(turns, fakellm.TextTurn(offlineAnswer))
		}
		return fakellm.New(spec.ID, turns...), nil
	}
	if !Configured(spec) {
		return nil, fmt.Errorf("%w: %s", ErrProviderNotConfigured, spec.Provider)
	}
	return BuildModel(ctx, spec)
}

// footer друкує застереження, без яких таблицю прочитають неправильно.
func footer(live bool, c Catalog) string {
	mode := "офлайн (скриптована модель): латентність НЕ вимірює провайдера, " +
		"токени й вартість — оцінені за токенайзером із каталогу"
	if live {
		mode = "live: латентність і токени — від провайдера, там де він їх повертає"
	}
	return fmt.Sprintf("Режим: %s.\nКаталог: станом на %s. %s\n", mode, c.AsOf, c.Source)
}
