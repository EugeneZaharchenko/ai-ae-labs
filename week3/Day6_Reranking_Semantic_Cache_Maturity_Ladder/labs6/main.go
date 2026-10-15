// Стартовий шаблон ДЗ 6 — ADK Go v2.5.0 (вересень 2026), Go 1.27 (пін — go.mod
// у корені репозиторію). Перевірте актуальність API перед стартом: лінія
// релізів рухається щомісяця.
//
// Граф без LLM — запускається БЕЗ API-ключа (re-rank через модель додасте самі):
//
//	go run . console                          // корпус: testdata/chunks.json
//	CHUNKS=../../Day5_.../chunks.json go run . console   // ваш індекс із ДЗ 5
//	go test ./...                             // скелет табличних тестів — main_test.go
//	OLLAMA_EMBED_MODEL=embeddinggemma go run . console   // ембединги через Ollama
//
// Основано на https://github.com/google/adk-go/blob/v2.5.0/examples/workflow/basic/main.go
package main

import (
	"context"
	"errors"
	"log"
	"os"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/cmd/launcher/full"

	"github.com/dimetron/ai-eng-course/labs/internal/adkenv"
	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/corpus"
	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/embed"
)

func main() {
	// apps/.env — необов'язковий: без нього ембединги на чистому Go.
	if err := adkenv.Load("."); err != nil && !errors.Is(err, adkenv.ErrNotFound) {
		log.Fatal(err)
	}
	chunks, err := corpus.Load(chunksPath(os.Getenv("CHUNKS")))
	if err != nil {
		log.Fatal(err)
	}
	emb := embed.New()
	log.Printf("embeddings: %s", emb.Name())
	wa, err := newGraph(newRetriever(chunks, emb))
	if err != nil {
		log.Fatalf("failed to create workflow: %v", err)
	}
	l := full.NewLauncher()
	if err := l.Execute(context.Background(), &launcher.Config{AgentLoader: agent.NewSingleLoader(wa)}, os.Args[1:]); err != nil {
		log.Fatalf("Run failed: %v\n\n%s", err, l.CommandLineSyntax())
	}
}
