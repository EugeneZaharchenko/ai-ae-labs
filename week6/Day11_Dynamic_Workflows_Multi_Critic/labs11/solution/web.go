// Живий Web UI для еталона: той самий динамічний агент, що в RunTask
// (maker → gate → панель критиків), але за ADK-лаунчером.
//
//	go run ./solution -task parse_amount web webui api   # http://localhost:8080/ui/ + /api
//	go run ./solution -offline console                   # той самий агент у терміналі
//
// Слова після прапорців — аргументи лаунчера. Завдання береться з -task, а не
// з тексту в чаті: цикл ігнорує вхідне повідомлення, тож надішліть будь-яке,
// щоб запустити прогін. Слід спроб видно в Events (StateDelta).
//
// Офлайн-модель — скрипт на ОДИН прогін (заглушка → хибне → еталон). Другий
// прогін у тій самій сесії вичерпає скрипт, тому для повторного перезапустіть
// сервер. Із живою моделлю обмеження немає.
package main

import (
	"context"
	"fmt"
	"io"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/cmd/launcher/full"

	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/billing"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/gate"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/usage"
)

// launch — шов для тестів: справжній лаунчер слухає порт.
var launch = func(ctx context.Context, a agent.Agent, args []string) error {
	l := full.NewLauncher()
	if err := l.Execute(ctx, &launcher.Config{AgentLoader: agent.NewSingleLoader(a)}, args); err != nil {
		return fmt.Errorf("launcher: %w\n\n%s", err, l.CommandLineSyntax())
	}
	return nil
}

// serve збирає агента під -task і віддає його лаунчеру.
func serve(ctx context.Context, cfg config, out io.Writer, res Resolver) error {
	task, ok := billing.ByID(cfg.task)
	if !ok {
		return fmt.Errorf("невідоме завдання %q; доступні: %s", cfg.task, taskIDs())
	}
	models, offline := pickModels(ctx, cfg, res, out)
	if offline {
		models = OfflineModels(task)
	}
	fmt.Fprintf(out, "завдання: %s\n", task.ID)

	a, err := NewAgent(Config{
		Maker:       models.Maker,
		Critics:     DefaultCritics(models.Critics[RoleCorrectness], models.Critics[RoleStyle], models.Critics[RolePerformance]),
		Grader:      ExecGrader{Runner: gate.Runner{}, Test: task.Visible},
		MaxAttempts: cfg.maxAttempts,
		MaxTokens:   cfg.maxTokens,
		Meter:       &usage.Meter{},
		Tests:       task.Visible,
	})
	if err != nil {
		return err
	}
	return launch(ctx, a, cfg.serve)
}
