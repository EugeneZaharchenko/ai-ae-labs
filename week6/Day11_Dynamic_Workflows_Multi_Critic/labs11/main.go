// ДЗ 11 — Multi-Critic Coding Agent (динамічний граф ADK Go v2).
//
// Цикл: maker пише код → compiler-критик (go build/vet/test) → якщо зелене,
// три LLM-критики (correctness, style, security) → якщо хоч один проти,
// його коментарі йдуть у Draft.Feedback і maker пробує ще раз (до maxAttempts).
//
//	go run ./week6/Day11_Dynamic_Workflows_Multi_Critic/labs11 -task prorate_refund
//	go run ./week6/Day11_Dynamic_Workflows_Multi_Critic/labs11 -all          # eval-таблиця по 3 задачах
//	go run ./week6/Day11_Dynamic_Workflows_Multi_Critic/labs11 -all -seq     # те саме, критики послідовно
//	go run ./week6/Day11_Dynamic_Workflows_Multi_Critic/labs11 -offline      # без ключа, скриптовані моделі
//	go run ./week6/Day11_Dynamic_Workflows_Multi_Critic/labs11 -task parse_amount web webui api
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/cmd/launcher/full"
	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/billing"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/usage"
)

// maxAttempts — бюджет циклу. Не «100 про всяк випадок»: якщо агент не
// впорався за 4 спроби, п'ята рахунок збільшить, а результат — ні.
const maxAttempts = 4

// Draft — робота maker'а разом із зауваженнями, які він має врахувати.
type Draft struct {
	Task     string   `json:"task"`
	Tests    string   `json:"tests"`
	Attempt  int      `json:"attempt"`
	Code     string   `json:"code,omitempty"`
	Feedback []string `json:"feedback,omitempty"`
}

// Verdict — типізований вердикт критика (перевірка — ParseVerdict у verdict.go).
type Verdict struct {
	Critic   string   `json:"critic"`
	Passed   bool     `json:"passed"`
	Comments []string `json:"comments,omitempty"`
}

const makerInstruction = `Ти пишеш Go-код для billing-сервісу LEDGERWORKS. На вхід — JSON:
task (що написати), tests (тести, які код ОБОВ'ЯЗКОВО проходить — це
специфікація), code (твоя попередня версія) і feedback (що не так; префікс
[compiler] — це сказав компілятор або тест, [correctness]/[style]/[security] — рев'юери).
Відповідай ЛИШЕ вмістом одного файлу (package billing), без пояснень і без
тестів. Якщо feedback не порожній — виправ рівно те, що там написано;
[compiler] виправляй першим. Лише стандартна бібліотека, жодних TODO.`

func main() {
	taskID := flag.String("task", "prorate_refund", "id завдання: prorate_refund, parse_amount, validate_iban")
	all := flag.Bool("all", false, "проґнати всі 3 задачі й надрукувати eval-таблицю")
	seq := flag.Bool("seq", false, "критики по черзі, а не паралельно (для порівняння часу)")
	offline := flag.Bool("offline", false, "скриптовані моделі навіть якщо є ключ")
	flag.Parse()

	ctx := context.Background()
	llm, live := pickModel(ctx, *offline)
	opt := runOptions{maxAttempts: maxAttempts, parallel: !*seq, progress: os.Stdout}
	modelsFor := func(t billing.Task) models {
		if !live {
			return offlineModels(t) // scripts are used up, so a new set per task
		}
		return models{maker: llm, critics: map[string]model.LLM{
			RoleCorrectness: llm, RoleStyle: llm, RoleSecurity: llm,
		}}
	}

	tasks := billing.Tasks()
	if !*all {
		t, ok := billing.ByID(*taskID)
		if !ok {
			log.Fatalf("невідоме завдання %q", *taskID)
		}
		tasks = []billing.Task{t}
	}

	// Слова після прапорців (`web webui api`, `console`) — режим лаунчера ADK.
	// Завдання береться з -task, текст у чаті цикл ігнорує.
	if args := flag.Args(); len(args) > 0 {
		a, err := buildAgent(tasks[0], modelsFor(tasks[0]), opt, &usage.Meter{})
		if err != nil {
			log.Fatalf("агент: %v", err)
		}
		l := full.NewLauncher()
		if err := l.Execute(ctx, &launcher.Config{AgentLoader: agent.NewSingleLoader(a)}, args); err != nil {
			log.Fatalf("Run failed: %v\n\n%s", err, l.CommandLineSyntax())
		}
		return
	}

	mode := "паралельно"
	if *seq {
		mode = "послідовно"
	}
	var results []taskResult
	for _, t := range tasks {
		fmt.Printf("\n═══ задача %s (maxAttempts=%d, критики %s) ═══\n", t.ID, maxAttempts, mode)
		r := runTask(ctx, t, modelsFor(t), opt)
		printResult(r)
		results = append(results, r)
	}
	if len(results) > 1 {
		fmt.Printf("\n═══ eval (критики %s) ═══\n%s", mode, evalTable(results))
	}
}

// pickModel takes the live model from apps/.env (internal/modelcfg).
// No provider → scripted models, and the output says why.
func pickModel(ctx context.Context, forceOffline bool) (model.LLM, bool) {
	if forceOffline {
		fmt.Println("ОФЛАЙН: запитано -offline. Моделі скриптовані, gate і граф — справжні.")
		return nil, false
	}
	_ = modelcfg.LoadEnv(".")
	llm, choice, err := modelcfg.Load(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ОФЛАЙН: провайдера немає (%v).\nМоделі скриптовані, gate і граф — справжні.\n",
			strings.SplitN(err.Error(), "\n", 2)[0])
		return nil, false
	}
	fmt.Printf("модель (maker і всі критики): %s / %s (%s)\n", choice.Provider, choice.Model, choice.Reason)
	return llm, true
}

func printResult(r taskResult) {
	fmt.Println("── event log (session state, StateDelta) ──")
	fmt.Print(eventLog(r.Events))
	fmt.Println("── підсумок ──")
	fmt.Printf("прийнято: %t, спроб: %d, раундів критиків: %d, час: %.1fs, holdout: %s\n",
		r.Accepted, r.Attempts, r.CriticRounds, r.Elapsed.Seconds(), holdoutCell(r))
	if r.Err != "" {
		fmt.Printf("помилка циклу: %s\n", r.Err)
	}
	fmt.Printf("токени (UsageMetadata, output разом із thinking):\n%s", r.Meter.Table())
	if r.Code != "" {
		fmt.Printf("\nприйнятий код:\n%s\n", r.Code)
	}
}

// stripFence знімає markdown-огорожу, якщо модель її додала попри інструкцію.
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i:]
		if nl := strings.Index(s, "\n"); nl >= 0 {
			s = s[nl+1:]
		}
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	return s
}
