// Стартовий шаблон ДЗ 5 — ADK Go v2.5.0 (жовтень 2026), Go 1.27 (пін — go.mod
// у корені репозиторію). Перевірте актуальність API перед стартом: лінія
// релізів рухається щомісяця.
//
// Граф без LLM — запускається БЕЗ API-ключа. Введіть шлях до документа:
//
//	go run . console      // потім: testdata/ledgerworks_soc2.md
//	go test ./...         // скелет табличних тестів — main_test.go
//
// PDF → Markdown робить docling-mcp (див. README.md, розділ «PDF через
// docling-mcp»). Markdown-файли docling не потребують.
//
// Основано на https://github.com/google/adk-go/blob/v2.5.0/examples/workflow/basic/main.go
package main

import (
	"context"
	"log"
	"os"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/cmd/launcher/full"

	"github.com/dimetron/ai-eng-course/labs/week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/docling"
)

func main() {
	conv := docling.NewFromEnv()
	defer conv.Close()

	wa, err := newGraph(conv, NewStores())
	if err != nil {
		log.Fatalf("failed to create workflow: %v", err)
	}
	l := full.NewLauncher()
	if err := l.Execute(context.Background(), &launcher.Config{AgentLoader: agent.NewSingleLoader(wa)}, os.Args[1:]); err != nil {
		log.Fatalf("Run failed: %v\n\n%s", err, l.CommandLineSyntax())
	}
}
