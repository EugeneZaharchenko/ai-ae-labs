// Стартовий шаблон ДЗ 11 — станом на ADK Go v2.6.0 (10/2026), перевірте актуальність API.
//
// Що вже працює (ваш ранній win, ≤15 хв):
//   - maker — справжній LlmAgent на моделі з internal/modelcfg (apps/.env або
//     Ollama), загорнутий у вузол графа;
//   - gate — справжні go build / go vet / go test над кодом maker'а
//     (internal/gate, уже написаний; код моделі — недовірений вхід);
//   - динамічний цикл generate → gate → feedback з бюджетом спроб.
//
// Що робите ВИ (див. TODO): панель із ≥3 LLM-критиків із різними ролями й
// моделями, валідація вердиктів, агрегація, токенний бюджет з UsageMetadata.
//
//	go run ./week6/Day11_Dynamic_Workflows_Multi_Critic/labs11 -task validate_iban
//
// Без провайдера програма не падає: maker стає скриптованим, а вивід прямо
// каже чому (див. Homework.md, «Якщо щось не працює»).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/billing"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/gate"
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

// Verdict — типізований вердикт критика.
// TODO(студент): ParseVerdict(raw, role) — відсікає невідому роль, поле critic,
// що не збігається з роллю вузла, passed=false без comments і passed=true з
// comments. Несумісний вердикт НЕ «якось інтерпретується».
type Verdict struct {
	Critic   string   `json:"critic"`
	Passed   bool     `json:"passed"`
	Comments []string `json:"comments,omitempty"`
}

const makerInstruction = `Ти пишеш Go-код для billing-сервісу LEDGERWORKS. На вхід — JSON:
task (що написати), tests (тести, які код ОБОВ'ЯЗКОВО проходить — це
специфікація), code (твоя попередня версія) і feedback (що не так).
Відповідай ЛИШЕ вмістом одного файлу (package billing), без пояснень і без
тестів. Якщо feedback не порожній — виправ рівно те, що там написано.
Лише стандартна бібліотека.`

func main() {
	taskID := flag.String("task", "validate_iban", "id завдання: prorate_refund, parse_amount, validate_iban")
	flag.Parse()
	task, ok := billing.ByID(*taskID)
	if !ok {
		log.Fatalf("невідоме завдання %q", *taskID)
	}

	ctx := context.Background()
	llm := makerModel(ctx, task)

	// Maker: справжній LlmAgent у вузлі графа з власним таймаутом.
	// Вузол типізований: на вході Draft (ADK віддає його моделі як JSON).
	makerAgent, err := llmagent.New(llmagent.Config{
		Name:        "maker",
		Model:       llm,
		Description: "Пише і виправляє код за завданням та зауваженнями.",
		Instruction: makerInstruction,
	})
	if err != nil {
		log.Fatalf("maker: %v", err)
	}
	makerNode, err := workflow.NewAgentNodeTyped[Draft, string](makerAgent, workflow.NodeConfig{})
	if err != nil {
		log.Fatalf("вузол maker: %v", err)
	}

	// TODO(студент): панель критиків. Підказка: для кожної ролі — llmagent з
	// OutputSchema (genai.Schema під Verdict) і власною Instruction/моделлю;
	// три вузли → workflow.NewEdgeBuilder().AddFanOut(Start, …).AddFanIn(join, …)
	// → workflow.NewWorkflowNode("critic_panel", …); викликати з циклу через
	// workflow.RunNode(..., workflow.WithUseSubBranch()).

	g := gate.Runner{}
	loop := workflow.NewDynamicNode[string, string]("multi_critic_loop",
		func(ctx agent.Context, _ string, emit func(*session.Event) error) (string, error) {
			d := Draft{Task: task.Prompt, Tests: task.Visible}
			for d.Attempt = 1; d.Attempt <= maxAttempts; d.Attempt++ {
				out, err := workflow.RunNode[string](ctx, makerNode, d, workflow.WithUseSubBranch())
				if err != nil {
					return "", fmt.Errorf("maker (спроба %d): %w", d.Attempt, err)
				}
				d.Code = stripFence(out)

				rep := g.Grade(ctx, gate.Spec{Code: d.Code, Test: task.Visible})
				fmt.Printf("спроба %d: gate ok=%t stage=%q\n", d.Attempt, rep.OK, rep.Stage)
				if !rep.OK {
					// Сирий вивід інструмента — найкращий фідбек: maker виправляє
					// те, що сказав компілятор, а не пересказ.
					d.Feedback = []string{"[grader] " + rep.Stage + ": " + rep.Output}
					continue
				}
				// TODO(студент): тут — панель критиків над d. Зберіть УСІ
				// вердикти, провалідуйте, агрегуйте (кворум: correctness блокує).
				// Якщо не пройшли — Feedback = коментарі, continue.
				return d.Code, nil
			}
			return "", fmt.Errorf("бюджет вичерпано: %d спроб без проходження перевірок", maxAttempts)
		},
		workflow.NodeConfig{},
	)

	wa, err := workflowagent.New(workflowagent.Config{
		Name:        "multi_critic_coding_agent",
		Description: "Динамічний цикл maker → gate → (критики) → фідбек.",
		Edges:       workflow.Chain(workflow.Start, loop),
	})
	if err != nil {
		log.Fatalf("workflowagent: %v", err)
	}

	res, err := labrun.Run(ctx, wa, task.Prompt)
	if err != nil {
		log.Fatalf("цикл: %v", err)
	}
	fmt.Printf("\nприйнятий код:\n%s\n", res.Final)
	// TODO(студент): токени з UsageMetadata (AfterModelCallback) і eval-таблиця
	// по всіх трьох завданнях: прийнято / спроб / holdout (task.Holdout) / токенів.
}

// makerModel вибирає живу модель; без провайдера — скрипт, і каже чому.
func makerModel(ctx context.Context, task billing.Task) model.LLM {
	_ = modelcfg.LoadEnv(".")
	llm, choice, err := modelcfg.Load(ctx)
	if err == nil {
		fmt.Printf("maker: %s / %s (%s)\n", choice.Provider, choice.Model, choice.Reason)
		return llm
	}
	fmt.Fprintf(os.Stderr, "ОФЛАЙН: провайдера немає (%v).\nmaker скриптований: заглушка → еталон. Gate і граф — справжні.\n\n",
		strings.SplitN(err.Error(), "\n", 2)[0])
	return fakellm.New("offline-maker",
		fakellm.TextTurn("package billing\n\n// TODO\n"), fakellm.TextTurn(task.Reference))
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
