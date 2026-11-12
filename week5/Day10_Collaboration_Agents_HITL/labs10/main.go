// ДЗ 10 — «Collaborative Workflow Engine» команди LEDGERWORKS.
// Станом на ADK Go v2.5.0 (11/2026).
//
// Координатор + три спеціалісти (risk / compliance / integration) паралельно,
// JoinNode зводить розділи в порядку плану, затвор схвалення ставить ран на
// паузу, а рішення людини живе в журналі (internal/approvals, SQLite).
//
//	go run .                                   # demo: start + відповідь зі stdin
//	go run . start                             # процес 1: до паузи
//	go run . -by oksana decide <id> approve    # процес 2: рішення + відновлення
//	go run . -down integration_desk start      # джерело мовчить → прогалина
//	go run . audit <session>                   # події, журнал, токени
//
// Модель обирає internal/modelcfg (apps/.env, MODEL, DEFAULT_MODEL_PROVIDER).
// Без провайдера програма працює на ОФЛАЙН-ФЕЙКУ й каже про це першим рядком.
package main

import (
	"context"
	"log"
	"os"

	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, resolveModel); err != nil {
		log.Fatal(err)
	}
}

// resolveModel — справжній провайдер або названий офлайн-фейк (nil: кожен
// агент отримає власний скрипт).
func resolveModel(ctx context.Context) (model.LLM, string) {
	if err := modelcfg.LoadEnv("."); err != nil {
		log.Printf("попередження: %v", err)
	}
	m, choice, err := modelcfg.Load(ctx)
	if err != nil {
		return nil, "ОФЛАЙН-ФЕЙК (справжньої LLM немає): " + err.Error() + ". Додайте ключ в apps/.env — див. Homework.md."
	}
	return m, "модель: " + choice.Reason
}
