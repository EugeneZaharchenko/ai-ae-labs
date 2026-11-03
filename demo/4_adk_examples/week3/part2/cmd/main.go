// Agent for Week 3, Part 2: AgenticGraphRAGEngine
// Hybrid retrieval with planner, reranker, and semantic cache.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/internal/adkrun"
)

// QueryPlan represents a decomposed query plan.
type QueryPlan struct {
	OriginalQuery string   `json:"original_query"`
	SubQueries    []string `json:"sub_queries"`
	Strategy      string   `json:"strategy"` // "vector", "graph", "hybrid"
}

// RetrievalResult holds a single retrieval result.
type RetrievalResult struct {
	ChunkID string  `json:"chunk_id"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
	Source  string  `json:"source"` // "vector" or "graph"
}

// semanticCache is a simple in-memory semantic cache.
type semanticCache struct {
	mu   sync.RWMutex
	data map[string]string
}

var cache = &semanticCache{data: make(map[string]string)}

func (c *semanticCache) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.data[key]
	return v, ok
}

func (c *semanticCache) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = value
}

// planQuery decomposes a query into sub-queries and selects strategy.
func planQuery(ctx agent.Context, query string) (QueryPlan, error) {
	strategy := "hybrid"
	subQueries := []string{query}

	if strings.Contains(strings.ToLower(query), "who") ||
		strings.Contains(strings.ToLower(query), "signed") {
		strategy = "graph"
		subQueries = append(subQueries, "find relationships: "+query)
	} else if strings.Contains(strings.ToLower(query), "what") ||
		strings.Contains(strings.ToLower(query), "explain") {
		strategy = "vector"
	}

	return QueryPlan{
		OriginalQuery: query,
		SubQueries:    subQueries,
		Strategy:      strategy,
	}, nil
}

// retrieveVector performs vector retrieval (simulated).
func retrieveVector(ctx agent.Context, plan QueryPlan) ([]RetrievalResult, error) {
	return []RetrievalResult{
		{ChunkID: "v1", Content: fmt.Sprintf("Vector result for: %s", plan.SubQueries[0]), Score: 0.85, Source: "vector"},
		{ChunkID: "v2", Content: "Related semantic content", Score: 0.72, Source: "vector"},
	}, nil
}

// contractRow is one row of the LEDGERWORKS acquirer-contract fixture — the
// same corpus Week 3 Day 5 parses (merchant / tariff / regulatory requirement /
// signer). Signer names come from the fictional counterparty list, not the cast.
type contractRow struct {
	Merchant    string
	Tariff      string
	Requirement string // "" when the tariff is not subject to the requirement
	Signer      string
	ChunkID     string
}

var contractRows = []contractRow{
	{Merchant: "A-114", Tariff: "T-2", Requirement: "NBU-2026", Signer: "L. Doroshenko, Head of Partnerships, Acme Bank", ChunkID: "c-A114-sign"},
	{Merchant: "A-207", Tariff: "T-2", Requirement: "NBU-2026", Signer: "L. Doroshenko, Head of Partnerships, Acme Bank", ChunkID: "c-A207-sign"},
	{Merchant: "A-331", Tariff: "T-1", Requirement: "", Signer: "M. Sidliar, Head of Merchant Operations, Globex Acquiring", ChunkID: "c-A331-sign"},
}

var tariffRe = regexp.MustCompile(`(?i)\bT-?(\d+)\b`)

// retrieveGraph performs graph retrieval over the contract fixture:
// Tariff → Merchant → Contract → Signer. When the query names a tariff code,
// only contracts on that tariff are traversed — A-331 (T-1) must not leak into
// a T-2 answer. Without a tariff code it falls back to a generic traversal.
func retrieveGraph(ctx agent.Context, plan QueryPlan) ([]RetrievalResult, error) {
	m := tariffRe.FindStringSubmatch(plan.OriginalQuery)
	if m == nil {
		return []RetrievalResult{
			{ChunkID: "g1", Content: fmt.Sprintf("Graph traversal for: %s", plan.OriginalQuery), Score: 0.91, Source: "graph"},
		}, nil
	}
	tariff := "T-" + m[1]
	var out []RetrievalResult
	for _, r := range contractRows {
		if r.Tariff != tariff {
			continue
		}
		req := "not subject to NBU 2026"
		if r.Requirement != "" {
			req = "subject to " + r.Requirement
		}
		out = append(out, RetrievalResult{
			ChunkID: r.ChunkID,
			Content: fmt.Sprintf("Merchant %s on tariff %s (%s) → Signer=%s", r.Merchant, r.Tariff, req, r.Signer),
			Score:   0.91,
			Source:  "graph",
		})
	}
	return out, nil
}

// rerankResults applies cross-encoder reranking.
func rerankResults(ctx agent.Context, results []RetrievalResult) ([]RetrievalResult, error) {
	// Simulated cross-encoder reranking: boost graph results slightly.
	for i := range results {
		if results[i].Source == "graph" {
			results[i].Score += 0.05
		}
	}
	// Sort by score descending (simple bubble sort for clarity).
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Score > results[i].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}
	return results, nil
}

// checkCache consults the semantic cache before retrieval. It passes the plan
// through unchanged: a cache hit cannot skip the fan-out here without a route,
// so the hit is only reported on stderr. The node keeps the plan type so the
// two retrievers can hang off it.
func checkCache(ctx agent.Context, plan QueryPlan) (QueryPlan, error) {
	if _, ok := cache.Get(plan.OriginalQuery); ok {
		fmt.Fprintf(os.Stderr, "cache hit for %q\n", plan.OriginalQuery)
	}
	return plan, nil
}

// mergeResults flattens the JoinNode output (map keyed by predecessor name)
// into one slice. Branch values arrive as decoded JSON, so each one is
// round-tripped into []RetrievalResult. Keys are sorted for a stable order.
func mergeResults(ctx agent.Context, branches map[string]any) ([]RetrievalResult, error) {
	names := make([]string, 0, len(branches))
	for name := range branches {
		names = append(names, name)
	}
	sort.Strings(names)

	var merged []RetrievalResult
	for _, name := range names {
		raw, err := json.Marshal(branches[name])
		if err != nil {
			return nil, fmt.Errorf("merge %s: %w", name, err)
		}
		var part []RetrievalResult
		if err := json.Unmarshal(raw, &part); err != nil {
			return nil, fmt.Errorf("merge %s: %w", name, err)
		}
		merged = append(merged, part...)
	}
	return merged, nil
}

// synthesizeResponse combines results into final answer.
func synthesizeResponse(ctx agent.Context, results []RetrievalResult) (string, error) {
	var b strings.Builder
	b.WriteString("## Retrieval Results\n\n")
	for _, r := range results {
		b.WriteString(fmt.Sprintf("- [%s] (score: %.2f) %s\n", r.Source, r.Score, r.Content))
	}
	b.WriteString("\nProvenance: chunks ")

	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.ChunkID
	}
	b.WriteString(strings.Join(ids, ", "))
	b.WriteString("\n")
	return b.String(), nil
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("graphrag-engine", flag.ContinueOnError)
	query := fs.String("query", "which merchants on tariff T-2 fall under NBU 2026 and who signed their contracts", "search query")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Build a workflow with fan-out for vector + graph retrieval.
	planNode := workflow.NewFunctionNode[string, QueryPlan]("planner", planQuery, adkrun.NodeConfig())
	cacheNode := workflow.NewFunctionNode[QueryPlan, QueryPlan]("cache_check", checkCache, adkrun.NodeConfig())
	vectorNode := workflow.NewFunctionNode[QueryPlan, []RetrievalResult]("vector_retriever", retrieveVector, adkrun.NodeConfig())
	graphNode := workflow.NewFunctionNode[QueryPlan, []RetrievalResult]("graph_retriever", retrieveGraph, adkrun.NodeConfig())
	joinNode := workflow.NewJoinNode("joiner")
	mergeNode := workflow.NewFunctionNode[map[string]any, []RetrievalResult]("merger", mergeResults, adkrun.NodeConfig())
	rerankNode := workflow.NewFunctionNode[[]RetrievalResult, []RetrievalResult]("reranker", rerankResults, adkrun.NodeConfig())
	synthNode := workflow.NewFunctionNode[[]RetrievalResult, string]("synthesizer", synthesizeResponse, adkrun.NodeConfig())

	// plan → cache → (vector + graph) → join → merge → rerank → synthesize
	eb := workflow.NewEdgeBuilder()
	eb.Add(workflow.Start, planNode)
	eb.Add(planNode, cacheNode)
	eb.AddFanOut(cacheNode, vectorNode, graphNode)
	eb.AddFanIn(joinNode, vectorNode, graphNode)
	eb.Add(joinNode, mergeNode)
	eb.Add(mergeNode, rerankNode)
	eb.Add(rerankNode, synthNode)

	a, err := workflowagent.New(workflowagent.Config{
		Name:        "AgenticGraphRAGEngine",
		Description: "Hybrid retrieval with planner, reranker, and semantic cache",
		Edges:       eb.Build(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	out, err := adkrun.Run(context.Background(), a, *query, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Println(out)
	return 0
}
