package main

import (
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/workflow"
)

// newGraph збирає ingestion-конвеєр — це файл, де ви читаєте й змінюєте
// топологію (для ++ A3 — fan-out на кілька документів через JoinNode):
//
//	Start → load → parse → chunk → extract_entities → index_vector → index_graph → report
//
// Без LLM і без API-ключа: кожен вузол — локальна Go-функція з типізованим
// входом і виходом.
//
// st — сховища, які наповнюють вузли index_*; main.go і тести передають
// NewStores().
func newGraph(conv MarkdownConverter, st Stores) (agent.Agent, error) {
	// Без retry: вузли локальні, а помилка парсингу не зникне від повтору.
	cfg := workflow.NodeConfig{}
	edges := workflow.Chain(
		workflow.Start,
		workflow.NewFunctionNode("load", load, cfg),
		workflow.NewFunctionNode("parse", newParse(conv), cfg),
		workflow.NewFunctionNode("chunk", chunk, cfg),
		workflow.NewFunctionNode("extract_entities", extractEntities, cfg),
		workflow.NewFunctionNode("index_vector", newIndexVector(st.Vector), cfg),
		workflow.NewFunctionNode("index_graph", newIndexGraph(st.Graph), cfg),
		workflow.NewFunctionNode("report", report, cfg),
	)
	return workflowagent.New(workflowagent.Config{
		Name:        "hierarchical_chunker",
		Description: "Ingestion-конвеєр: документ → Markdown → ієрархічні чанки → сутності → індекси → статистика.",
		Edges:       edges,
	})
}
