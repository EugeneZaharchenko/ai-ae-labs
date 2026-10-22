package main

import (
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/workflow"
)

// newGraph builds the agentic retrieval graph:
//
//	Start → cache_check ─hit──→ cached
//	                    └miss─→ classify ─multi-hop─→ decompose ─┐
//	                                     └─other classes─────────┤
//	         ┌───────────────────────────────────────────────────┘
//	         ├─ vector_search ──┐
//	         ├─ keyword_search ─┤
//	         ├─ graph_filter ───├─ join → rerank ─grounded─────→ format
//	         └─ graph_traversal ┘                └insufficient─→ insufficient
//
// Every branch always runs (a JoinNode waits for all predecessors); the plan
// tells a branch whether it is needed, an unused one returns Skipped.
func newGraph(r *retriever) (agent.Agent, error) {
	// No retries: all nodes are local, a retry gives the same result.
	cfg := workflow.NodeConfig{}
	cacheCheck := workflow.NewFunctionNode("cache_check", r.cacheCheck, cfg)
	cached := workflow.NewFunctionNode("cached", cachedAnswer, cfg)
	classifyNode := workflow.NewEmittingFunctionNode("classify", classify, cfg)
	decomposeNode := workflow.NewFunctionNode("decompose", decompose, cfg)
	vector := workflow.NewFunctionNode("vector_search", r.vectorSearch, cfg)
	keyword := workflow.NewFunctionNode("keyword_search", r.keywordSearch, cfg)
	filter := workflow.NewFunctionNode("graph_filter", r.graphFilter, cfg)
	traversal := workflow.NewFunctionNode("graph_traversal", r.graphTraversal, cfg)
	join := workflow.NewJoinNode("join")
	rerank := workflow.NewFunctionNode("rerank", r.rerankNode, cfg)
	format := workflow.NewFunctionNode("format", r.format, cfg)
	noEvidence := workflow.NewFunctionNode("insufficient", insufficient, cfg)

	// The other classes skip decompose and fan out straight from classify.
	oneHop := workflow.MultiRoute[string]{classSingleHop, classGlobal, classMetadata}
	edges := workflow.NewEdgeBuilder().
		Add(workflow.Start, cacheCheck).
		AddRoute(cacheCheck, cached, workflow.StringRoute("hit")).
		AddRoute(cacheCheck, classifyNode, workflow.StringRoute("miss")).
		AddRoute(classifyNode, decomposeNode, workflow.StringRoute(classMultiHop)).
		AddFanOut(decomposeNode, vector, keyword, filter, traversal).
		AddRoute(classifyNode, vector, oneHop).
		AddRoute(classifyNode, keyword, oneHop).
		AddRoute(classifyNode, filter, oneHop).
		AddRoute(classifyNode, traversal, oneHop).
		AddFanIn(join, vector, keyword, filter, traversal).
		Add(join, rerank).
		AddRoute(rerank, format, workflow.StringRoute("grounded")).
		AddRoute(rerank, noEvidence, workflow.StringRoute("insufficient")).
		Build()

	return workflowagent.New(workflowagent.Config{
		Name:        "agentic_graphrag",
		Description: "Agentic GraphRAG: кеш → планувальник → vector/BM25/graph гілки → RRF + rerank → відповідь із provenance.",
		Edges:       edges,
	})
}
