package main

import (
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/workflow"
)

// newGraph збирає retrieval-конвеєр — це файл, де ви змінюєте топологію.
//
// Стартер — лінійний ланцюжок:
//
//	Start → search → rerank → answer
//
// TODO(студент): Homework п.1 — agentic-граф через workflow.NewEdgeBuilder:
//
//	Start → cache_check → classify ─┬─ vector_search   ─┐
//	                                ├─ graph_filter     ├─ JoinNode → rerank → format
//	                                └─ graph_traversal ─┘
//
// classify — workflow.NewEmittingFunctionNode з маршрутами single-hop /
// multi-hop / global / metadata-filter; збір гілок — workflow.NewJoinNode.
// Еталон fan-out + JoinNode:
// https://github.com/google/adk-go/blob/v2.4.0/examples/workflow/complex/main.go
func newGraph(r *retriever) (agent.Agent, error) {
	// Без retry: вузли локальні, повтор не змінить результат.
	cfg := workflow.NodeConfig{}
	edges := workflow.Chain(
		workflow.Start,
		workflow.NewFunctionNode("search", r.search, cfg),
		workflow.NewFunctionNode("rerank", r.rerank, cfg),
		workflow.NewFunctionNode("answer", r.answer, cfg),
	)
	return workflowagent.New(workflowagent.Config{
		Name:        "high_precision_rag",
		Description: "Retrieval-конвеєр: пошук → re-rank → відповідь із provenance + кеш відповідей.",
		Edges:       edges,
	})
}
