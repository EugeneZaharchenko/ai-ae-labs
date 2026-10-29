// Живий Web UI для воркера: той самий дослідницький агент, якого цикл
// самополіпшення вимірює, але з чинною стратегією з strategy.md.
//
//	go run ./solution web webui api     # http://localhost:8080/ui/  +  /api
//	go run ./solution console           # той самий агент у терміналі
//
// Усе після першого слова — аргументи лаунчера ADK. Робоча тека береться зі
// змінної LAB12_ROOT (типово "."): там лежить strategy.md, який цикл
// `--experiments=N` оновлює при keep. Тож щоб побачити ефект самополіпшення:
// прожени цикл, потім відкрий Web UI і постав те саме питання.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/cmd/launcher/full"

	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/ledgerkb"
)

// isServeCmd каже, чи перший аргумент — режим лаунчера, а не прапорець циклу.
func isServeCmd(args []string) bool {
	return len(args) > 0 && (args[0] == "web" || args[0] == "console")
}

// launch — шов для тестів: справжній лаунчер слухає порт.
var launch = func(ctx context.Context, a agent.Agent, args []string) error {
	l := full.NewLauncher()
	if err := l.Execute(ctx, &launcher.Config{AgentLoader: agent.NewSingleLoader(a)}, args); err != nil {
		return fmt.Errorf("launcher: %w\n\n%s", err, l.CommandLineSyntax())
	}
	return nil
}

// serve піднімає воркера за чинною стратегією. Невалідний strategy.md —
// помилка старту, а не мовчазний відкат до базової: інакше Web UI показував би
// не ту стратегію, яку бачить людина у файлі.
func serve(ctx context.Context, args []string, out io.Writer) error {
	root := os.Getenv("LAB12_ROOT")
	if root == "" {
		root = "."
	}
	absRoot, err := absDir(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return fmt.Errorf("створення %s: %w", root, err)
	}
	path := filepath.Join(absRoot, "strategy.md")
	seeded, err := ensureStrategy(path)
	if err != nil {
		return err
	}
	if seeded {
		fmt.Fprintln(out, "створено базовий strategy.md (його не було)")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("читання strategy.md: %w", err)
	}
	doc, err := ParseStrategy(string(raw), ledgerkb.AllToolNames())
	if err != nil {
		return fmt.Errorf("strategy.md: %w", err)
	}

	models := resolveModels(ctx, "strategy.md")
	fmt.Fprintln(out, "модель:", models.Label)
	fmt.Fprintf(out, "стратегія: %s (%s)\n", doc.Title, path)

	worker, err := NewWorkerAgent(models.Worker, ledgerkb.New(), doc, 0)
	if err != nil {
		return err
	}
	log.SetOutput(out)
	return launch(ctx, worker, args)
}
