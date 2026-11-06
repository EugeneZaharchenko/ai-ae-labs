package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/corpus"
)

const oksanaQuery = oksanaQueryText

func runGraph(t *testing.T, r *retriever, query string) labrun.Result {
	t.Helper()
	a, err := newGraph(r)
	if err != nil {
		t.Fatalf("newGraph: %v", err)
	}
	res, err := labrun.Run(context.Background(), a, query)
	if err != nil {
		t.Fatalf("run %q: %v", query, err)
	}
	return res
}

func TestClassify(t *testing.T) {
	t.Parallel()
	tests := []struct{ query, want string }{
		{oksanaQuery, classMultiHop},
		{"Хто підписав договір мерчанта A-331?", classMultiHop},
		{"Які мерчанти на тарифі T-1?", classMetadata},
		{"Чи підпадає A-331 під вимогу НБУ 2026?", classMetadata},
		{"Що таке тариф T-2?", classSingleHop},
		{"Хто підписав договір CT-2025-031?", classSingleHop},
		{"Дай короткий огляд документів", classGlobal},
	}
	for _, tt := range tests {
		if got := classifyQuery(tt.query); got != tt.want {
			t.Errorf("classifyQuery(%q) = %s, want %s", tt.query, got, tt.want)
		}
	}
}

// Oksana's query must return both sentences: the merchants on T-2 under the
// NBU 2026 requirement, and the signer of every contract, each with a citation.
func TestGraph_OksanaMultiHop(t *testing.T) {
	t.Parallel()
	res := runGraph(t, newTestRetriever(t), oksanaQuery)
	for _, want := range []string{
		"A-114, A-207",
		"c-nbu-req, nbu_2026_requirements, span",
		"CT-2025-014", "Олег Мельник", "c-sign-014, ledgerworks_soc2, span",
		"CT-2025-031", "Ірина Бойко", "c-sign-031, ledgerworks_soc2, span",
		"plan=multi-hop steps=5/5",
	} {
		if !strings.Contains(res.Final, want) {
			t.Errorf("no %q in answer:\n%s", want, res.Final)
		}
	}
	if strings.Contains(res.Final, "A-331") {
		t.Errorf("A-331 is on T-1 and must not be in the answer:\n%s", res.Final)
	}
	// All retriever branches ran inside the same invocation.
	for _, node := range []string{"vector_search", "graph_filter", "graph_traversal"} {
		if !eventFrom(res, node) {
			t.Errorf("no event from %s in the event log", node)
		}
	}
}

func eventFrom(res labrun.Result, node string) bool {
	for _, e := range res.Events {
		if e.Author == node || strings.Contains(e.Branch, node) {
			return true
		}
	}
	return false
}

func TestGraph_BudgetExhausted(t *testing.T) {
	t.Parallel()
	r := newTestRetriever(t)
	p := Plan{Query: oksanaQuery, Class: classMultiHop, TextQuery: oksanaQuery, Filter: parseFilter(oksanaQuery), Budget: 4}
	p, _ = decompose(newNodeCtx(), p)
	res, err := r.graphTraversal(newNodeCtx(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || res.Steps != 2 {
		t.Fatalf("budget 4 leaves 2 hops: steps=%d truncated=%v", res.Steps, res.Truncated)
	}
	out, _ := insufficient(newNodeCtx(), Evidence{Plan: p, Steps: 4, Truncated: true})
	if !strings.Contains(out, "Недостатньо доказів") || !strings.Contains(out, "budget") {
		t.Errorf("got %q", out)
	}
	if grounded(Evidence{Plan: p, Steps: 4, Truncated: true, Facts: res.Facts}) {
		t.Error("a truncated walk must not count as grounded")
	}
}

func TestGraph_OutOfDomain(t *testing.T) {
	t.Parallel()
	res := runGraph(t, newTestRetriever(t), "Яка погода в Києві?")
	if !strings.Contains(res.Final, "Недостатньо доказів") {
		t.Errorf("out-of-domain query must end in insufficient evidence:\n%s", res.Final)
	}
}

func TestGraph_MerchantNotUnderNBU(t *testing.T) {
	t.Parallel()
	res := runGraph(t, newTestRetriever(t), "Чи підпадає A-331 під вимогу НБУ 2026?")
	for _, want := range []string{"A-331 на тарифі T-1", "НЕ підпадає", "c-nbu-req"} {
		if !strings.Contains(res.Final, want) {
			t.Errorf("no %q in:\n%s", want, res.Final)
		}
	}
}

func TestGraph_CacheParaphrase(t *testing.T) {
	t.Parallel()
	r := newTestRetriever(t)
	a, err := newGraph(r)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := labrun.Run(ctx, a, "Скільки коштує тариф T-2?")
	if err != nil {
		t.Fatal(err)
	}
	second, err := labrun.Run(ctx, a, "Яка ціна тарифу T-2?")
	if err != nil {
		t.Fatal(err)
	}
	other, err := labrun.Run(ctx, a, "Яка ціна тарифу T-1?")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Final, "[cache_hit=false]") || !strings.Contains(second.Final, "[cache_hit=true]") {
		t.Errorf("want miss then hit:\n%s\n---\n%s", first.Final, second.Final)
	}
	if strings.Contains(other.Final, "[cache_hit=true]") {
		t.Errorf("T-1 must not reuse the T-2 answer:\n%s", other.Final)
	}
	if s := r.cache.snapshot(); s.Semantic != 1 || s.Miss != 2 {
		t.Errorf("stats = %+v, want 1 semantic hit and 2 misses", s)
	}
}

// --- Cache -------------------------------------------------------------------

func TestCache_NoFalsePositive(t *testing.T) {
	t.Parallel()
	pairs := []struct{ cached, asked string }{
		{"Що таке тариф T-2?", "Що таке тариф T-1?"},
		{"Яка ставка комісії на тарифі T-2?", "Які мерчанти на тарифі T-2?"},
		{"Хто підписав договір CT-2025-031?", "Хто підписав договір CT-2025-014?"},
		{"Що таке тариф T-2?", "Хто підписав договір мерчанта A-114?"},
	}
	for _, p := range pairs {
		r := newTestRetriever(t)
		ctx := context.Background()
		if err := r.cache.store(ctx, r.emb, r.scope, p.cached, "answer"); err != nil {
			t.Fatal(err)
		}
		if _, kind, _ := r.cache.lookup(ctx, r.emb, r.scope, p.asked); kind != "" {
			t.Errorf("%q reused the answer of %q (%s hit)", p.asked, p.cached, kind)
		}
	}
}

func TestCache_ScopeAndTTL(t *testing.T) {
	t.Parallel()
	r := newTestRetriever(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	r.cache.now = func() time.Time { return now }
	q := "Що таке тариф T-2?"
	if err := r.cache.store(ctx, r.emb, r.scope, q, "answer"); err != nil {
		t.Fatal(err)
	}
	if _, kind, _ := r.cache.lookup(ctx, r.emb, r.scope, q); kind != "exact" {
		t.Fatalf("same scope: kind = %q, want exact", kind)
	}
	other := r.scope
	other.Tenant = "another_bank"
	if _, kind, _ := r.cache.lookup(ctx, r.emb, other, q); kind != "" {
		t.Error("another tenant must not see the entry")
	}
	newVersion := r.scope
	newVersion.CorpusVersion = "v2"
	if _, kind, _ := r.cache.lookup(ctx, r.emb, newVersion, q); kind != "" {
		t.Error("a new corpus version must not see the entry")
	}
	now = now.Add(defaultCacheTTL + time.Minute)
	if _, kind, _ := r.cache.lookup(ctx, r.emb, r.scope, q); kind != "" {
		t.Error("expired entry was returned")
	}
	r.cache.invalidate("v2")
	if len(r.cache.entries) != 0 {
		t.Errorf("invalidate kept %d entries of the old version", len(r.cache.entries))
	}
}

func TestCorpusVersion(t *testing.T) {
	t.Parallel()
	r := newTestRetriever(t)
	v1 := corpusVersion(r.chunks)
	changed := append([]corpus.Chunk(nil), r.chunks...)
	changed[0].Text += "!"
	if v2 := corpusVersion(changed); v1 == v2 {
		t.Error("changed corpus kept the same version")
	}
}

// --- Retrieval ---------------------------------------------------------------

func TestBM25(t *testing.T) {
	t.Parallel()
	got := newTestRetriever(t).bm25.search("договір CT-2025-031 підписала", 3)
	if len(got) == 0 || got[0].ChunkID != "c-sign-031" {
		t.Fatalf("BM25 top-1 = %v, want c-sign-031", got)
	}
}

func TestFuse_RRF(t *testing.T) {
	t.Parallel()
	got := fuse([]RetrievalResult{
		{Candidates: []Candidate{{ChunkID: "a", Score: 900}, {ChunkID: "b", Score: 1}}},
		{Candidates: []Candidate{{ChunkID: "b", Score: 0.9}, {ChunkID: "c", Score: 0.8}}},
	})
	// b is in both lists, so it wins although its BM25 score is tiny.
	if got[0].ChunkID != "b" || len(got) != 3 {
		t.Fatalf("fuse = %v", got)
	}
}

func TestDecodeJoin_Errors(t *testing.T) {
	t.Parallel()
	if _, err := decodeJoin(map[string]any{}); err == nil {
		t.Error("empty join must fail")
	}
	if _, err := decodeJoin(map[string]any{"vector_search": "not a result"}); err == nil {
		t.Error("bad branch output must fail")
	}
}

func TestVectorSearch(t *testing.T) {
	t.Parallel()
	res, err := newTestRetriever(t).vectorSearch(newNodeCtx(), Plan{TextQuery: "договір CT-2025-031 підписала Ірина Бойко"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) == 0 || res.Candidates[0].ChunkID != "c-sign-031" || res.Steps != 1 {
		t.Fatalf("vector top-1 = %+v", res.Candidates[:1])
	}
}
