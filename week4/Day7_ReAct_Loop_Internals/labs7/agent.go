// ГОТОВО, ЧИТАЙТЕ: справжній ReAct-агент на штатному ADK — llmagent + functiontool.
//
// Це той самий цикл Thought → Action → Observation, який ви напишете руками в
// react.go, але тут його крутить рушій: internal/llminternal/base_flow.go,
// Flow.Run — це `for {}`, що на кожному кроці (runOneStep) кличе модель
// (callLLM), виконує її tool calls (handleFunctionCalls) і повертає
// FunctionResponse у розмову. Зупиняється він лише тоді, коли модель відповіла
// без tool call (IsFinalResponse).
//
// Тому одне ви бачите тут уже зараз: ліміту кроків у рушія НЕМАЄ. Його дає
// BeforeModelCallback нижче — без нього агент на вашому API-ключі крутився б,
// доки модель сама не вирішить зупинитись. Саме цю межу «що робить рушій /
// що лишається вам» ви сьогодні проведете руками.
//
//	go run . agent                                   # справжня модель (apps/.env)
//	go run . agent "Investigate merchant A-114 ..."  # своє завдання
//
// Перевірено проти google.golang.org/adk/v2 v2.6.0 (станом на 10/2026).
package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/ledger"
	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/react"
	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/scripted"
)

const defaultTask = "Investigate the payout incident for merchant A-114 (since 03:00) and file one follow-up ticket."

// keyModelCalls — лічильник кроків у стані сесії. Префікс temp: — стан одного
// рану: наступний запит користувача починає з нуля.
const keyModelCalls = "temp:react_model_calls"

// Result — вихід усіх трьох інструментів. functiontool перетворить його на
// FunctionResponse {"result": "..."} — саме це модель прочитає як Observation.
type Result struct {
	Result string `json:"result"`
}

// NewADKAgent збирає ReAct-агента на штатному llmagent.
func NewADKAgent(m model.LLM, l *ledger.Ledger, maxSteps int) (agent.Agent, error) {
	return newAgent(m, l, maxSteps, nil)
}

// NewGuardedAgent — той самий агент плюс охорона інструмента з побічним ефектом
// (guards.go): flow guard завжди, LLM guard — якщо передано суддю.
func NewGuardedAgent(m model.LLM, l *ledger.Ledger, maxSteps int, j Judge) (agent.Agent, error) {
	return newAgent(m, l, maxSteps, toolGuard(j))
}

func newAgent(m model.LLM, l *ledger.Ledger, maxSteps int, guard llmagent.BeforeToolCallback) (agent.Agent, error) {
	tools, err := ledgerTools(l)
	if err != nil {
		return nil, err
	}
	return llmagent.New(llmagent.Config{
		Name:                 "ledgerworks_investigator",
		Description:          "Investigates LEDGERWORKS payout incidents with read-only tools and files one follow-up ticket.",
		Model:                m,
		Instruction:          react.SystemPrompt,
		Tools:                tools,
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{stepCap(keyModelCalls, maxSteps)},
		BeforeToolCallbacks:  guards(guard),
	})
}

// stepCap — ліміт викликів моделі як BeforeModelCallback. Ключ у стані свій для
// кожного агента: у графі з кількома llmagent кожен вузол має власний ліміт, а
// не спільний лічильник на весь ран.
func stepCap(key string, maxSteps int) llmagent.BeforeModelCallback {
	return func(ctx agent.Context, _ *model.LLMRequest) (*model.LLMResponse, error) {
		n := 0
		if v, err := ctx.State().Get(key); err == nil {
			n, _ = v.(int)
		}
		if n >= maxSteps {
			// Помилка тут зупиняє ран ДО виклику моделі: жодного токена.
			return nil, fmt.Errorf("вичерпано ліміт %d викликів моделі: %w", maxSteps, ErrMaxIterations)
		}
		return nil, ctx.State().Set(key, n+1)
	}
}

func guards(g llmagent.BeforeToolCallback) []llmagent.BeforeToolCallback {
	if g == nil {
		return nil
	}
	return []llmagent.BeforeToolCallback{g}
}

// ledgerTools оголошує три інструменти LEDGERWORKS як ADK-інструменти.
//
// Схему, яку бачить модель, functiontool виводить із типізованих аргументів
// (ledger.WikiArgs і т.д., теги json/jsonschema), і валідує аргументи ДО
// виклику функції. У мануальному циклі ту саму схему ви пишете руками
// (ledger.Tools → genai.Schema) — порівняйте.
func ledgerTools(l *ledger.Ledger) ([]tool.Tool, error) {
	wiki, err := functiontool.New(
		functiontool.Config{Name: "search_internal_wiki", Description: "Searches the internal wiki (runbooks, postmortems)."},
		func(_ agent.Context, in ledger.WikiArgs) (Result, error) {
			return Result{Result: l.SearchWiki(in.Query)}, nil
		})
	if err != nil {
		return nil, fmt.Errorf("build wiki tool: %w", err)
	}
	payouts, err := functiontool.New(
		functiontool.Config{Name: "get_merchant_payouts", Description: "Reads the most recent payouts of one merchant. Returns a page; pass rows to see more."},
		func(_ agent.Context, in ledger.PayoutsArgs) (Result, error) {
			s, err := l.Payouts(in.MerchantID, in.Since, in.Rows)
			return Result{Result: s}, err
		})
	if err != nil {
		return nil, fmt.Errorf("build payouts tool: %w", err)
	}
	ticket, err := functiontool.New(
		functiontool.Config{Name: "create_jira_ticket", Description: "Creates a follow-up Jira ticket and returns its key (LDG-N)."},
		func(_ agent.Context, in ledger.TicketArgs) (Result, error) {
			key, err := l.CreateTicket(in.Title, in.Body)
			return Result{Result: key}, err
		})
	if err != nil {
		return nil, fmt.Errorf("build ticket tool: %w", err)
	}
	return []tool.Tool{wiki, payouts, ticket}, nil
}

// runAgent — `go run . agent`: справжня модель, якщо провайдер налаштований.
// Інакше — скриптована модель, і про це сказано вголос: офлайн-прогін ніколи не
// має виглядати як відповідь моделі.
func runAgent(ctx context.Context, out io.Writer, task string) error {
	if strings.TrimSpace(task) == "" {
		task = defaultTask
	}
	m, judge := agentModel(ctx, out)
	l := ledger.New()
	a, err := NewGuardedAgent(m, l, maxIterations, judge)
	if err != nil {
		return err
	}
	res, runErr := labrun.Run(ctx, a, task)
	printEvents(out, res)
	fmt.Fprintf(out, "tickets: %v\n", l.Tickets())
	return runErr
}

// agentModel вибирає модель для агента й суддю для його охорони. Справжня
// модель — є і LLM guard; без неї — скриптована модель і лише flow guard, і про
// це сказано вголос: «суддя» без моделі була б вигадка.
func agentModel(ctx context.Context, out io.Writer) (model.LLM, Judge) {
	if m, ok := react.ResolveModel(ctx, out); ok {
		fmt.Fprintln(out, "  охорона: flow guard + LLM guard (guards.go)")
		return m, LLMJudge(m)
	}
	fmt.Fprintln(out, "  (скриптована модель: показує форму циклу, а НЕ відповідь моделі)")
	fmt.Fprintln(out, "  охорона: лише flow guard — суддею без моделі була б вигадка")
	return offlineModel(), nil
}

// teamModels — те саме для графа з трьох llmagent: одна справжня модель на всі
// вузли або по скрипту на вузол.
func teamModels(ctx context.Context, out io.Writer) (TeamModels, Judge) {
	if m, ok := react.ResolveModel(ctx, out); ok {
		fmt.Fprintln(out, "  охорона: flow guard + LLM guard на вузлі reporter (guards.go)")
		return TeamModels{Wiki: m, Payouts: m, Reporter: m}, LLMJudge(m)
	}
	fmt.Fprintln(out, "  (скриптована модель на кожен вузол: показує форму графа, а НЕ відповідь моделі)")
	fmt.Fprintln(out, "  охорона: лише flow guard — суддею без моделі була б вигадка")
	ms, _ := offlineTeam()
	return ms, nil
}

// runTeam — `go run . team`: той самий LEDGERWORKS-інцидент, але граф із трьох
// llmagent. У трасі видно, який вузол що зробив — це й є «ReAct усередині вузла».
func runTeam(ctx context.Context, out io.Writer, task string) error {
	if strings.TrimSpace(task) == "" {
		task = defaultTask
	}
	ms, judge := teamModels(ctx, out)
	l := ledger.New()
	a, err := NewTeamAgent(ms, l, maxIterations, judge)
	if err != nil {
		return err
	}
	res, runErr := labrun.Run(ctx, a, task)
	printEvents(out, res)
	var nodes []string
	seen := map[string]bool{}
	for _, e := range res.Events {
		if e.Author != "" && e.Author != "user" && !seen[e.Author] {
			seen[e.Author] = true
			nodes = append(nodes, e.Author)
		}
	}
	fmt.Fprintf(out, "nodes: %s\n", strings.Join(nodes, " → "))
	fmt.Fprintf(out, "tickets: %v\n", l.Tickets())
	return runErr
}

// printEvents друкує журнал подій сесії як трасу Thought → Action → Observation.
// Кожна дія і кожне спостереження несе свій CallID: так рушій зіставляє
// результат із викликом, коли їх кілька в одному кроці.
func printEvents(out io.Writer, res labrun.Result) {
	calls, tokens := 0, 0
	for _, e := range res.Events {
		if u := e.LLMResponse.UsageMetadata; u != nil {
			calls++
			tokens += int(u.TotalTokenCount)
		}
		if e.LLMResponse.Content == nil {
			continue
		}
		for _, p := range e.LLMResponse.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				fmt.Fprintf(out, "  act [%s] %s %v\n", p.FunctionCall.ID, p.FunctionCall.Name, p.FunctionCall.Args)
			case p.FunctionResponse != nil:
				fmt.Fprintf(out, "  obs [%s] %s: %s\n", p.FunctionResponse.ID, p.FunctionResponse.Name, firstLine(fmt.Sprint(p.FunctionResponse.Response["result"])))
			case p.Thought && p.Text != "":
				fmt.Fprintf(out, "  thought: %s\n", firstLine(p.Text))
			}
		}
	}
	fmt.Fprintf(out, "answer: %s\n", res.Final)
	fmt.Fprintf(out, "model calls: %d, tokens: %d (з UsageMetadata)\n", calls, tokens)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

// offlineModel — скрипт: два незалежні читання в одному кроці, потім запис,
// потім відповідь. Токени звітує, щоб журнал мав ту саму форму, що й живий.
func offlineModel() *scripted.Model {
	return scripted.New("scripted",
		scripted.Call(420,
			scripted.FC("c1", "search_internal_wiki", map[string]any{"query": "payout incident"}),
			scripted.FC("c2", "get_merchant_payouts", map[string]any{"merchant_id": "A-114", "since": "03:00"})),
		scripted.Call(610, scripted.FC("c3", "create_jira_ticket", map[string]any{
			"title": "payout incident follow-up", "body": "see incident #418"})),
		scripted.Say("Related incident #418 found; acquirer says unrelated. Ticket LDG-1 filed.", 540),
	)
}
