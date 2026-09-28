// ДЗ 3: звичайний запуск використовує реальну модель; -mode=graph — без LLM.
// Скриптована модель є тільки у тестах. ADK Go v2.4.0, станом на 09/2026.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/cmd/launcher/full"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/week2/internal/refund"
)

const demoInput = "Мерчант A-114 просить повернення по транзакції txn-2026-07-118845"

func runDemo(ctx context.Context, out io.Writer, input string) error {
	a, err := newGraph(&refund.Registry{})
	if err != nil {
		return err
	}
	return runAgent(ctx, a, out, input)
}

func runAgent(ctx context.Context, a agent.Agent, out io.Writer, input string) error {
	result, err := labrun.Run(ctx, a, input)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	// Omit timestamps and invocation IDs; keep real outputs, business deltas
	// and the selected route. Without Routes the classification event would
	// echo the request and discard the decision this lab exists to audit.
	for _, event := range result.Events {
		row := struct {
			Author     string         `json:"author"`
			Routes     []string       `json:"routes,omitempty"`
			Output     any            `json:"output,omitempty"`
			Content    *genai.Content `json:"content,omitempty"`
			StateDelta map[string]any `json:"state_delta,omitempty"`
		}{event.Author, event.Routes, event.Output, event.Content, event.Actions.StateDelta}
		if err := enc.Encode(row); err != nil {
			return fmt.Errorf("write audit event: %w", err)
		}
	}
	return nil
}

func main() {
	ctx := context.Background()
	mode := flag.String("mode", "live", "live (configured model) or graph (no model)")
	flag.Parse()
	var a agent.Agent
	var err error
	switch *mode {
	case "live":
		if err := modelcfg.LoadEnv("."); err != nil {
			log.Fatal(err)
		}
		m, choice, loadErr := modelcfg.Load(ctx)
		if loadErr != nil {
			log.Fatalf("live mode: %v\nFor the explicit offline path use -mode=graph.", loadErr)
		}
		log.Printf("live model: %s", choice.Reason)
		a, err = newLiveAgent(m, &refund.Registry{})
	case "graph":
		a, err = newGraph(&refund.Registry{})
	default:
		log.Fatalf("unknown mode %q; use live or graph", *mode)
	}
	if err != nil {
		log.Fatal(err)
	}
	if flag.NArg() == 0 {
		runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if err := runAgent(runCtx, a, os.Stdout, demoInput); err != nil {
			log.Fatal(err)
		}
		return
	}
	l := full.NewLauncher()
	if err := l.Execute(ctx, &launcher.Config{AgentLoader: agent.NewSingleLoader(a)}, flag.Args()); err != nil {
		log.Fatalf("%v\n%s", err, l.CommandLineSyntax())
	}
}
