// ГОТОВО, ЧИТАЙТЕ: граф із кількома llmagent — і ReAct усередині кожного вузла.
//
// agent.go — один llmagent: рушій крутить цикл Reason → Act → Observation сам.
// Тут їх три, а граф лише розставляє їх у порядку (так само, як у штатному
// прикладі ADK examples/workflow/complex):
//
//	Start ─┬─> wiki_researcher    ──┐   кожен — llmagent зі СВОЇМ набором інструментів
//	       └─> payouts_researcher ──┴─> gather ─> brief ─> reporter (create_jira_ticket)
//
// Хто що робить. ГРАФ вирішує, ХТО працює і в якому порядку (fan-out, join,
// передача виходу далі) — це топологія, і вона не змінюється під час рану.
// ReAct-цикл — усередині кожного вузла: researcher сам вирішує, скільки разів
// кликати свій інструмент, і Flow.Run (див. лекцію) зупиняє його, коли модель
// відповіла без tool call. Ліміт кроків і охорона інструмента ставляться на
// КОЖЕН вузол окремо — тими самими колбеками, що й в agent.go.
//
//	go run . team            # справжня модель (або скриптована, якщо ключа немає)
//
// Перевірено проти google.golang.org/adk/v2 v2.6.0 (станом на 10/2026).
package main

import (
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/ledger"
	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/scripted"
)

// Імена вузлів — це й імена агентів: за ними JoinNode віддає виходи, а в журналі
// подій видно, який вузол що зробив.
const (
	nodeWiki     = "wiki_researcher"
	nodePayouts  = "payouts_researcher"
	nodeReporter = "reporter"
)

const (
	wikiInstruction = `You are the wiki researcher of LEDGERWORKS, a payment processor.
Use search_internal_wiki to find runbooks and postmortems related to the incident in the request.
Reply with one to three sentences: what you found, with incident ids. Do not file tickets.`

	payoutsInstruction = `You are the payouts researcher of LEDGERWORKS, a payment processor.
Use get_merchant_payouts for the merchant named in the request and report anything late or pending, with batch numbers.
Reply with one to three sentences. Do not file tickets.`

	reporterInstruction = `You are the incident reporter of LEDGERWORKS.
You receive the findings of two researchers. File exactly one follow-up ticket with create_jira_ticket:
a title of at most 120 characters, and a body that cites the findings. Then reply with the ticket key.`
)

// TeamModels — модель кожного вузла. Наживо це одна модель, віддана тричі; у
// тестах — три скриптовані, щоб кожен вузол мав власний сценарій.
type TeamModels struct{ Wiki, Payouts, Reporter model.LLM }

// NewTeamAgent збирає граф із трьох llmagent. Ліміт кроків — на кожен вузол
// окремо; охорона інструмента (guards.go) стоїть на вузлі, що пише побічний ефект.
func NewTeamAgent(ms TeamModels, l *ledger.Ledger, maxSteps int, j Judge) (agent.Agent, error) {
	tools, err := ledgerTools(l) // wiki, payouts, ticket — у цьому порядку
	if err != nil {
		return nil, err
	}
	mk := func(name, desc, instr string, m model.LLM, ts []tool.Tool, guard llmagent.BeforeToolCallback) (agent.Agent, error) {
		return llmagent.New(llmagent.Config{
			Name:                 name,
			Description:          desc,
			Model:                m,
			Instruction:          instr,
			Tools:                ts,
			BeforeModelCallbacks: []llmagent.BeforeModelCallback{stepCap("temp:calls:"+name, maxSteps)},
			BeforeToolCallbacks:  guards(guard),
		})
	}
	wiki, err := mk(nodeWiki, "Searches the internal wiki for related incidents.", wikiInstruction, ms.Wiki, tools[0:1], nil)
	if err != nil {
		return nil, fmt.Errorf("build %s: %w", nodeWiki, err)
	}
	payouts, err := mk(nodePayouts, "Reads recent payouts of the merchant.", payoutsInstruction, ms.Payouts, tools[1:2], nil)
	if err != nil {
		return nil, fmt.Errorf("build %s: %w", nodePayouts, err)
	}
	reporter, err := mk(nodeReporter, "Files one follow-up ticket from the findings.", reporterInstruction, ms.Reporter, tools[2:3], toolGuard(j))
	if err != nil {
		return nil, fmt.Errorf("build %s: %w", nodeReporter, err)
	}

	// Агент у графі — це вузол. Без режиму llmagent у вузлі працює за один
	// «хід»: бере вхід вузла й повертає відповідь після власного ReAct-циклу.
	wikiNode, err := workflow.NewAgentNode(wiki, workflow.NodeConfig{})
	if err != nil {
		return nil, fmt.Errorf("node %s: %w", nodeWiki, err)
	}
	payoutsNode, err := workflow.NewAgentNode(payouts, workflow.NodeConfig{})
	if err != nil {
		return nil, fmt.Errorf("node %s: %w", nodePayouts, err)
	}
	reporterNode, err := workflow.NewAgentNode(reporter, workflow.NodeConfig{})
	if err != nil {
		return nil, fmt.Errorf("node %s: %w", nodeReporter, err)
	}
	gather := workflow.NewJoinNode("gather") // бар'єр: спрацьовує один раз, коли обидва дослідники готові
	brief := workflow.NewFunctionNode("brief", briefFindings, workflow.NodeConfig{})

	eb := workflow.NewEdgeBuilder()
	eb.AddFanOut(workflow.Start, wikiNode, payoutsNode)
	eb.AddFanIn(gather, wikiNode, payoutsNode)
	eb.Add(gather, brief)
	eb.Add(brief, reporterNode)

	return workflowagent.New(workflowagent.Config{
		Name:        "ledgerworks_team",
		Description: "Two researchers run in parallel, then a reporter files one ticket.",
		Edges:       eb.Build(),
		// Без SubAgents runner не вирішить автора подій вузлів і шумить у лог.
		SubAgents: []agent.Agent{wiki, payouts, reporter},
	})
}

// briefFindings перетворює виходи JoinNode (map[ім'я вузла]вихід) на один вхід
// для reporter. Фіксований порядок — щоб підказка була детермінованою.
func briefFindings(_ agent.Context, gathered map[string]any) (string, error) {
	var sb strings.Builder
	sb.WriteString("Findings:\n")
	for _, s := range []struct{ label, key string }{
		{"Wiki", nodeWiki},
		{"Payouts", nodePayouts},
	} {
		text := "(no findings)"
		if v, ok := gathered[s.key]; ok && v != nil {
			if t := strings.TrimSpace(fmt.Sprint(v)); t != "" {
				text = t
			}
		}
		fmt.Fprintf(&sb, "- %s: %s\n", s.label, text)
	}
	return sb.String(), nil
}

// offlineTeam — скрипт для кожного вузла: Reason → Act (виклик інструмента),
// потім Observation → відповідь. Дві моделі на вузол-дослідника — це і є
// ReAct-цикл усередині вузла.
func offlineTeam() (TeamModels, []*scripted.Model) {
	wiki := scripted.New("scripted-wiki",
		scripted.Call(120, scripted.FC("w1", "search_internal_wiki", map[string]any{"query": "payout incident"})),
		scripted.Say("Related incident #418: a late acquirer batch, ticketed before.", 90))
	payouts := scripted.New("scripted-payouts",
		scripted.Call(130, scripted.FC("p1", "get_merchant_payouts", map[string]any{"merchant_id": "A-114", "since": "03:00"})),
		scripted.Say("Batches #421 and #422 pending since 03:10; #418 settled late at 02:41.", 90))
	reporter := scripted.New("scripted-reporter",
		scripted.Call(150, scripted.FC("r1", "create_jira_ticket", map[string]any{
			"title": "payout incident follow-up", "body": "see incident #418; batches #421, #422 pending"})),
		scripted.Say("Ticket LDG-1 filed.", 60))
	return TeamModels{Wiki: wiki, Payouts: payouts, Reporter: reporter}, []*scripted.Model{wiki, payouts, reporter}
}
