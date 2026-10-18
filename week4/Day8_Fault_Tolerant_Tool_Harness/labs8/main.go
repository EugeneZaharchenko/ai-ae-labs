// Стартовий шаблон ДЗ 8 — станом на ADK Go v2.6.0 (жовтень 2026), перевірте актуальність API.
//
// Це крок 0: той самий агент, що в еталоні, але в одному процесі — без Temporal і
// без Dgraph. Тут ви пишете серце harness-а (п. 1–2 завдання); кроки 3–6 (durable
// цикл, сесія, пам'ять) ви додаєте новими файлами за зразком ./solution.
//
// Запуск (≤15 хв до першого результату):
//
//	LABS8_MODEL=demo go run . "Open a refund for txn-2026-07-118845 at merchant A-114"   # офлайн
//	go run . -faults 503,timeout "Open a refund for txn-2026-07-118845 at merchant A-114" # справжня модель
//
// Без LABS8_MODEL=demo модель обирає internal/modelcfg: apps/.env підхоплюється сам
// (шаблон — apps/.env-example), MODEL і DEFAULT_MODEL_PROVIDER працюють так само, як
// у лабах Тижня 1. Обрана модель друкується першим рядком. Якщо провайдера немає —
// програма каже про це й підказує LABS8_MODEL=demo.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/ledger"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/llm"
)

// Class — що harness робить із помилкою.
type Class int

const (
	ClassRetryTransient Class = iota + 1 // віддати рушію: повтор допоможе
	ClassSurfaceToModel                  // повернути моделі як Observation: повтор не допоможе
	ClassFatal                           // зупинитись
)

// maxSelfCorrections — скільки семантичних помилок один хід може перетворити
// на Observation, перш ніж harness зупинить ран.
const maxSelfCorrections = 3

// ErrSelfCorrectionExhausted обгортається помилкою, що закінчує такий ран.
var ErrSelfCorrectionExhausted = errors.New("self-correction budget exhausted")

// classifyError — ПУНКТ 1 (15 балів).
//
// TODO(студент): визначте клас через errors.Is / errors.As — ніколи через
// err.Error(). Підказки:
//   - context.Canceled → ClassFatal («зупинись», а не «повтори»);
//   - *ledger.SemanticError (errors.As) або errors.Is(err, ledger.ErrSemantic) → ClassSurfaceToModel;
//   - errors.Is(err, ledger.ErrTransient) або context.DeadlineExceeded → ClassRetryTransient;
//   - усе інше → ClassFatal: невідому помилку наосліп не повторюють.
//
// Другий результат — текст для логу або для моделі.
func classifyError(err error) (Class, string) {
	return ClassFatal, err.Error() // заглушка: поки що кожна помилка фатальна
}

// Observation — те, що модель читає після виклику, який не вдався.
type Observation struct {
	Status      string `json:"status"` // invalid_argument | unavailable | stopped
	Field       string `json:"field,omitempty"`
	Observation string `json:"observation"`
	Attempt     int    `json:"attempt,omitempty"`
}

// RefundResult — відповідь open_refund_case моделі: кейс або Observation.
type RefundResult struct {
	Status      string `json:"status"`
	CaseID      string `json:"case_id,omitempty"`
	Replayed    bool   `json:"replayed,omitempty"`
	Field       string `json:"field,omitempty"`
	Observation string `json:"observation,omitempty"`
	Attempt     int    `json:"attempt,omitempty"`
}

// budget рахує самокорекції в межах одного ходу. Він живе в стані ходу, а не в
// глобальній змінній: глобальний лічильник ламає паралельні тести.
type budget struct{ used int }

// observe — ПУНКТ 2 (15 балів).
//
// TODO(студент): семантична помилка НЕ повертається як error — вона стає
// Observation (status "invalid_argument", field, hint), а модель виправляє
// наступний хід. Збільшуйте b.used; коли b.used > maxSelfCorrections, поверніть
// error, що обгортає ErrSelfCorrectionExhausted (явний фейл, а не тиха зупинка).
// Транзієнтна помилка після вичерпання повторів → Observation{Status: "unavailable"}.
func (b *budget) observe(err error) (Observation, error) {
	return Observation{}, err // заглушка: прокидаємо все
}

// retry — стенд-ін для Temporal RetryPolicy. Крок 3 замінює його на Activity з
// RetryPolicy; тут він потрібен, щоб побачити поведінку без сервера.
// Повторюємо лише те, що classifyError назвав транзієнтним.
func retry(ctx context.Context, attempts int, fn func() (ledger.RefundCaseOutput, error)) (ledger.RefundCaseOutput, error) {
	var lastErr error
	for i := range attempts {
		out, err := fn()
		if err == nil {
			return out, nil
		}
		if class, _ := classifyError(err); class != ClassRetryTransient {
			return out, err
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ledger.RefundCaseOutput{}, ctx.Err()
		case <-time.After(time.Duration(i+1) * 100 * time.Millisecond):
		}
	}
	return ledger.RefundCaseOutput{}, lastErr
}

// openRefundCase будує інструмент: валідація й побічний ефект — у ledger.Desk
// (ключ ідемпотентності = transaction_id + merchant_id), політика — тут.
func openRefundCase(desk *ledger.Desk, b *budget) func(agent.Context, ledger.RefundCaseInput) (RefundResult, error) {
	return func(ctx agent.Context, in ledger.RefundCaseInput) (RefundResult, error) {
		out, err := retry(ctx, 4, func() (ledger.RefundCaseOutput, error) { return desk.OpenRefundCase(ctx, in) })
		if err != nil {
			obs, ferr := b.observe(err)
			if ferr != nil {
				return RefundResult{}, ferr
			}
			return RefundResult{Status: obs.Status, Field: obs.Field, Observation: obs.Observation, Attempt: obs.Attempt}, nil
		}
		return RefundResult{Status: out.Status, CaseID: out.CaseID, Replayed: out.Replayed}, nil
	}
}

const instruction = `You are the LEDGERWORKS refund desk agent. You only open refund cases.
Copy ids exactly as the user wrote them: merchants look like A-114, transactions like txn-2026-07-118845. Never send placeholders such as "undefined".
If a tool returns status "invalid_argument", read the observation, fix the named field from the user's message, and call the tool again.
If a tool returns status "unavailable" or "stopped", do not call it again; tell the user what happened.`

func main() {
	faultsFlag := flag.String("faults", "", "scripted acquirer faults, e.g. 503,timeout (a queue, not a probability)")
	flag.Parse()
	if flag.NArg() == 0 {
		log.Fatal("usage: go run . [-faults 503,timeout] MESSAGE")
	}
	ctx := context.Background()

	m, err := pickModel(ctx)
	if err != nil {
		log.Fatal(err)
	}
	faults := &ledger.FaultPlan{}
	for _, f := range strings.Split(*faultsFlag, ",") {
		switch strings.TrimSpace(f) {
		case "":
		case "503":
			faults.Push(ledger.Unavailable)
		case "timeout":
			faults.Push(ledger.Timeout)
		case "429":
			faults.Push(ledger.RateLimited)
		default:
			log.Fatalf("unknown fault %q (want 503, timeout, 429)", f)
		}
	}
	desk := &ledger.Desk{Cases: &ledger.MemCases{}, Faults: faults}

	b := &budget{}
	refund, err := functiontool.New(functiontool.Config{
		Name:        "open_refund_case",
		Description: "Opens a refund case for one transaction of one merchant. Needs transaction_id and merchant_id exactly as the user wrote them. May return status invalid_argument with an observation: read it, fix the named field, call again.",
	}, openRefundCase(desk, b))
	if err != nil {
		log.Fatal(err)
	}
	root, err := llmagent.New(llmagent.Config{
		Name: "refund_agent", Description: "Opens refund cases for LEDGERWORKS.",
		Model: m, Instruction: instruction, Tools: []tool.Tool{refund},
	})
	if err != nil {
		log.Fatal(err)
	}
	r, err := runner.New(runner.Config{AppName: "ledgerworks", Agent: root, SessionService: session.InMemoryService(), AutoCreateSession: true})
	if err != nil {
		log.Fatal(err)
	}

	msg := genai.NewContentFromText(strings.Join(flag.Args(), " "), genai.RoleUser)
	for ev, err := range r.Run(ctx, "taras", "s1", msg, agent.RunConfig{}) {
		if err != nil {
			log.Fatalf("[ev:failed] %v", err)
		}
		printEvent(ev)
	}
	fmt.Printf("[ev:success] self_corrections=%d\n", b.used)
}

// pickModel: LABS8_MODEL=demo — офлайн-модель на правилах (Sloppy відтворює
// інцидент 03:00); інакше — провайдер курсу через internal/modelcfg.
func pickModel(ctx context.Context) (model.LLM, error) {
	if os.Getenv("LABS8_MODEL") == "demo" {
		fmt.Println("model: demo-rules (offline, LABS8_MODEL=demo)")
		return llm.Demo{Sloppy: true}, nil
	}
	if err := modelcfg.LoadEnv("."); err != nil {
		fmt.Printf("warning: %v\n", err)
	}
	m, choice, err := modelcfg.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("немає LLM-провайдера: %w\nНалаштуйте ключ в apps/.env (див. apps/.env-example) або запустіть офлайн: LABS8_MODEL=demo go run . \"...\"", err)
	}
	fmt.Printf("model: %s\n", choice.Reason)
	return m, nil
}

func printEvent(ev *session.Event) {
	if ev == nil || ev.Content == nil {
		return
	}
	for _, p := range ev.Content.Parts {
		switch {
		case p == nil:
		case p.FunctionCall != nil:
			fmt.Printf("[ev:tool_call] %s %v\n", p.FunctionCall.Name, p.FunctionCall.Args)
		case p.FunctionResponse != nil:
			fmt.Printf("[ev:tool_observation] %s %v\n", p.FunctionResponse.Name, p.FunctionResponse.Response)
		case strings.TrimSpace(p.Text) != "":
			fmt.Printf("[ev:answer] %s\n", strings.TrimSpace(p.Text))
		}
	}
}
