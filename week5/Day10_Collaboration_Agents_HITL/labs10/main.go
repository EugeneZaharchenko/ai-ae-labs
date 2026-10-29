// Стартовий шаблон ДЗ 10 — «Collaborative Workflow Engine» команди LEDGERWORKS.
// Станом на ADK Go v2.6.0 (10/2026); перевірте актуальність API.
//
// Ранній win (≤15 хв) — один спеціаліст з ОДНИМ інструментом і справжньою моделлю:
//
//	go run .
//
// Модель обирає internal/modelcfg (apps/.env, MODEL, DEFAULT_MODEL_PROVIDER).
// Без провайдера програма працює на ОФЛАЙН-ФЕЙКУ й каже про це першим рядком —
// «працює» без моделі не те саме, що «працює з моделлю».
//
// Далі за TODO: ще двоє спеціалістів → fan-out + JoinNode → затвор схвалення.
// Еталон публікується після дедлайну.
package main

import (
	"context"
	"fmt"
	"log"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	// TODO(студент): "…/labs10/internal/approvals" — журнал схвалень для п.3.
)

// Finding — один факт із ідентифікатором джерела. Спеціаліст має право
// говорити лише те, що повернув інструмент.
type Finding struct {
	Claim  string `json:"claim"`
	Source string `json:"source"`
}

type riskInput struct {
	Topic string `json:"topic" jsonschema:"Тема, за якою потрібні факти"`
}

type riskOutput struct {
	Findings []Finding `json:"findings"`
}

// lookupRisk — фікстура бази інцидентів LEDGERWORKS. Справжня система
// ходила б у incident-db; форма (факт + джерело) та сама.
func lookupRisk(_ agent.Context, in riskInput) (riskOutput, error) {
	return riskOutput{Findings: []Finding{
		{Claim: "Cutover 2025-03 відкотили за 41 хв через розсинхрон токенів", Source: "incident-db:INC-418"},
		{Claim: "Rollback-ранбук для A-114 перевірено востаннє у вересні", Source: "runbook:cutover-rollback"},
	}}, nil
}

func main() {
	ctx := context.Background()
	m, label := resolveModel(ctx)
	fmt.Println(label)

	riskTool, err := functiontool.New(functiontool.Config{
		Name:        "lookup_risk_desk",
		Description: "Факти про ризики міграції з бази інцидентів.",
	}, lookupRisk)
	if err != nil {
		log.Fatal(err)
	}
	riskDesk, err := llmagent.New(llmagent.Config{
		Name:        "risk_desk_agent",
		Model:       m,
		Description: "Спеціаліст з ризиків міграції.",
		// TODO(студент): перепишіть інструкцію: інструмент ОБОВ'ЯЗКОВО першим,
		// відповідь лише фактами з нього, кожен факт — із джерелом у дужках.
		Instruction: "Виклич lookup_risk_desk і перекажи результат.",
		Tools:       []tool.Tool{riskTool},
	})
	if err != nil {
		log.Fatal(err)
	}

	res, err := labrun.Run(ctx, riskDesk, "Ризики міграції з legacy-процесора")
	if err != nil {
		log.Fatalf("ран: %v", err)
	}
	fmt.Println("інструменти:", res.ToolCalls)
	fmt.Println("відповідь:  ", res.Final)

	// TODO(студент) п.1: ще два спеціалісти — compliance_desk, integration_desk
	// (кожен зі своїм інструментом і своєю фікстурою). Інтеграційний нехай уміє
	// «не відповісти» (повернути помилку) — це ваш збій для п.6.
	//
	// TODO(студент) п.2: fan-out на трьох + workflow.NewJoinNode → вузол зведення
	// (NewEngine — ваш; Merge — ваша функція зведення результатів).
	//
	// TODO(студент) п.3: вузол-затвор через workflow.NewEmittingFunctionNode +
	// workflow.ResumeOrRequestInput; питання пишіть у approvals.Store, рішення
	// читайте ЗВІДТИ, не з відповіді в сесії (newApprovalGateNode — ваша).
}

// resolveModel — справжній провайдер або названий офлайн-фейк.
func resolveModel(ctx context.Context) (model.LLM, string) {
	if err := modelcfg.LoadEnv("."); err != nil {
		log.Printf("попередження: %v", err)
	}
	m, choice, err := modelcfg.Load(ctx)
	if err != nil {
		cause := err.Error()
		return fakellm.New("offline-risk",
				fakellm.CallTurn("lookup_risk_desk", map[string]any{"topic": "міграція"}),
				fakellm.TextTurn("Cutover 2025-03 відкотили за 41 хв (incident-db:INC-418)."),
			),
			"ОФЛАЙН-ФЕЙК (справжньої LLM немає): " + cause + ". Додайте ключ в apps/.env — див. Homework.md."
	}
	return m, "модель: " + choice.Reason
}
