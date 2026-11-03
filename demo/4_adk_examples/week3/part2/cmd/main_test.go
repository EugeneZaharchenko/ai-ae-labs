package main

import (
	"context"
	"testing"

	"google.golang.org/adk/v2/agent"
)

func TestPlanQuery_Hybrid(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	plan, err := planQuery(ctx, "what is onboarding")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != "vector" {
		t.Errorf("expected 'vector' strategy for 'what', got %q", plan.Strategy)
	}
}

func TestPlanQuery_Graph(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	plan, err := planQuery(ctx, "who signed the contract for merchant A-114")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != "graph" {
		t.Errorf("expected 'graph' strategy for 'who', got %q", plan.Strategy)
	}
}

func TestRetrieveVector(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	plan := QueryPlan{OriginalQuery: "test", SubQueries: []string{"test"}, Strategy: "vector"}
	results, err := retrieveVector(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	if results[0].Source != "vector" {
		t.Errorf("expected source 'vector', got %q", results[0].Source)
	}
}

func TestRetrieveGraph(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	plan := QueryPlan{OriginalQuery: "test", Strategy: "graph"}
	results, err := retrieveGraph(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	if results[0].Source != "graph" {
		t.Errorf("expected source 'graph', got %q", results[0].Source)
	}
}

// Week-3 spine query: T-2 → NBU 2026 → signer must return Doroshenko for
// A-114 and A-207 and must exclude A-331 (tariff T-1).
func TestRetrieveGraph_TariffT2_ReturnsDoroshenkoExcludesA331(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	plan := QueryPlan{
		OriginalQuery: "which merchants on tariff T-2 fall under NBU 2026 and who signed their contracts",
		Strategy:      "graph",
	}
	results, err := retrieveGraph(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 contracts on T-2, got %d: %+v", len(results), results)
	}
	for _, r := range results {
		if !contains(r.Content, "Doroshenko") {
			t.Errorf("expected signer Doroshenko, got %q", r.Content)
		}
		if !contains(r.Content, "subject to NBU-2026") {
			t.Errorf("expected NBU-2026 requirement on T-2 row, got %q", r.Content)
		}
		if contains(r.Content, "A-331") || contains(r.Content, "Sidliar") {
			t.Errorf("A-331 (T-1) leaked into a T-2 answer: %q", r.Content)
		}
	}
}

func TestRerankResults(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	results := []RetrievalResult{
		{ChunkID: "v1", Score: 0.7, Source: "vector"},
		{ChunkID: "g1", Score: 0.8, Source: "graph"},
	}
	reranked, err := rerankResults(ctx, results)
	if err != nil {
		t.Fatal(err)
	}
	if len(reranked) != 2 {
		t.Fatalf("expected 2 results, got %d", len(reranked))
	}
	// Graph result should be boosted and ranked first.
	if reranked[0].Source != "graph" {
		t.Errorf("expected graph result first after reranking")
	}
}

func TestSemanticCache(t *testing.T) {
	cache.Set("test query", "cached result")
	if got, ok := cache.Get("test query"); !ok || got != "cached result" {
		t.Errorf("cache miss: got %q, ok=%v", got, ok)
	}
}

func TestSynthesizeResponse(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	results := []RetrievalResult{
		{ChunkID: "v1", Content: "test content", Score: 0.9, Source: "vector"},
	}
	out, err := synthesizeResponse(ctx, results)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "test content") {
		t.Errorf("expected content in output, got: %s", out)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
