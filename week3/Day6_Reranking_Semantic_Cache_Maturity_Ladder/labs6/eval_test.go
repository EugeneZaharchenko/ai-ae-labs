package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/embed"
)

// evalRuns is how many times each query runs; the median is its latency.
const evalRuns = 5

func medianRow(t *testing.T, run func() (evalRow, error)) evalRow {
	t.Helper()
	var rows []evalRow
	for range evalRuns {
		row, err := run()
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(a, b evalRow) int { return int(a.Latency - b.Latency) })
	return rows[len(rows)/2]
}

// TestEval_AgenticVsBaseline runs the 20 gold queries through both systems.
// EVAL_OUT=eval.md writes the tables for the README.
func TestEval_AgenticVsBaseline(t *testing.T) {
	r := newTestRetriever(t)
	if os.Getenv("EVAL_OLLAMA") != "" {
		r = liveRetriever(t)
	}
	ctx := context.Background()
	var base, agentic []evalRow
	for _, q := range goldSet {
		base = append(base, medianRow(t, func() (evalRow, error) { return runBaseline(ctx, r, q) }))
		agentic = append(agentic, medianRow(t, func() (evalRow, error) { return runAgentic(ctx, r, q) }))
	}
	if len(goldSet) != 20 {
		t.Fatalf("gold set has %d queries, want 20", len(goldSet))
	}
	for _, kind := range []string{"single", "multi"} {
		b, a := summarize("baseline", kind, base), summarize("agentic", kind, agentic)
		if a.Groundness < b.Groundness {
			t.Errorf("%s: agentic groundedness %.2f < baseline %.2f", kind, a.Groundness, b.Groundness)
		}
	}
	for i := range agentic {
		t.Logf("%-70s base=%s %.2f agentic=%s %.2f", agentic[i].Query, base[i].Top1, base[i].Groundness, agentic[i].Top1, agentic[i].Groundness)
	}
	if m := summarize("agentic", "multi", agentic); m.Groundness < 0.99 {
		t.Errorf("multi-hop groundedness = %.2f, want 1.0", m.Groundness)
	}
	if out := os.Getenv("EVAL_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(evalMarkdown(t, r, base, agentic)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func evalMarkdown(t *testing.T, r *retriever, base, agentic []evalRow) string {
	var b strings.Builder
	b.WriteString("| # | Запит | Тип | top1 base | rel | grnd | top1 agentic | rel | grnd | tokens base / agentic |\n|---|---|---|---|---:|---:|---|---:|---:|---:|\n")
	for i := range base {
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %d | %.2f | %s | %d | %.2f | %d / %d |\n", i+1, base[i].Query, base[i].Kind,
			orDash(base[i].Top1), base[i].Relevance, base[i].Groundness,
			orDash(agentic[i].Top1), agentic[i].Relevance, agentic[i].Groundness, base[i].Tokens, agentic[i].Tokens)
	}
	b.WriteString("\n| Система | Тип | top1_relevance (1-5) | groundedness | latency_p95 | tokens_used (avg) |\n|---|---|---:|---:|---:|---:|\n")
	for _, kind := range []string{"single", "multi", "all"} {
		for _, s := range []evalSummary{summarize("vector-RAG", kind, base), summarize("agentic", kind, agentic)} {
			fmt.Fprintf(&b, "| %s | %s | %.2f | %.2f | %s | %.0f |\n", s.System, s.Kind, s.Relevance, s.Groundness, s.P95.Round(time.Microsecond), s.Tokens)
		}
	}
	b.WriteString("\n" + rerankMarkdown(t, r))
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// rerankQueries are the five before/after examples of the README: each names
// the first-stage retriever whose top-1 was wrong.
var rerankQueries = []struct {
	query, stage string
	want         []string
}{
	{"Хто підписав договір CT-2025-031?", "baseline", []string{"c-sign-031"}},
	{"Хто підписав договір CT-2025-031?", "rrf", []string{"c-sign-031"}},
	{"Що сталося зі знахідкою F-104?", "vector", []string{"c-F104"}},
	{"Що таке тариф T-1?", "vector", []string{"c-tariff-T1"}},
	{"Що сталося зі знахідкою F-101?", "baseline", []string{"c-F101"}},
}

// firstStage returns the candidate pool of one first-stage retriever.
func firstStage(t *testing.T, r *retriever, stage, query string) []Candidate {
	t.Helper()
	switch stage {
	case "baseline":
		res, err := newTestRetriever(t).search(newNodeCtx(), query)
		if err != nil {
			t.Fatal(err)
		}
		return res.Candidates
	case "vector":
		v, err := r.vectorTopK(context.Background(), query, poolK)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	return rerankPool(t, r, query)
}

// rerankPool runs the text branches and RRF, i.e. the pool before rerank.
func rerankPool(t *testing.T, r *retriever, query string) []Candidate {
	t.Helper()
	p := Plan{Query: query, TextQuery: query, Budget: maxSteps}
	v, err := r.vectorSearch(newNodeCtx(), p)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := r.keywordSearch(newNodeCtx(), p)
	return fuse([]RetrievalResult{v, k})
}

func rerankMarkdown(t *testing.T, r *retriever) string {
	var b strings.Builder
	b.WriteString("| # | Запит | Перший етап | top-1 до | top-1 після rerank | score після | grounded до / після |\n|---|---|---|---|---|---:|---|\n")
	for i, q := range rerankQueries {
		pool := firstStage(t, r, q.stage, q.query)
		after, err := r.rerankCandidates(context.Background(), q.query, pool)
		if err != nil {
			t.Fatal(err)
		}
		g := func(id string) string {
			if slices.Contains(q.want, id) {
				return "так"
			}
			return "ні"
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %.2f | %s / %s |\n", i+1, q.query, q.stage, pool[0].ChunkID, after[0].ChunkID, after[0].Score, g(pool[0].ChunkID), g(after[0].ChunkID))
	}
	return b.String()
}

// TestRerank_FiveQueries: on all five README examples the first stage puts a
// wrong chunk first and the re-ranker fixes it.
func TestRerank_FiveQueries(t *testing.T) {
	t.Parallel()
	r := newTestRetriever(t)
	for _, q := range rerankQueries {
		pool := firstStage(t, r, q.stage, q.query)
		after, err := r.rerankCandidates(context.Background(), q.query, pool)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(q.want, pool[0].ChunkID) {
			t.Errorf("%q (%s): first stage is already right, not a before/after example", q.query, q.stage)
		}
		if !slices.Contains(q.want, after[0].ChunkID) {
			t.Errorf("%q (%s): top-1 after rerank = %s, want %v", q.query, q.stage, after[0].ChunkID, q.want)
		}
	}
}

// TestQualityGate checks the calibrated gate on the gold set: every in-domain
// single-hop top-1 passes, the out-of-domain query does not.
func TestQualityGate(t *testing.T) {
	t.Parallel()
	r := newTestRetriever(t)
	for _, q := range goldSet {
		if q.Kind != "single" {
			continue
		}
		pool := rerankPool(t, r, q.Query)
		ranked, err := r.rerankCandidates(context.Background(), q.Query, pool)
		if err != nil {
			t.Fatal(err)
		}
		pass := ranked[0].Score >= qualityGate
		t.Logf("%-60s top1=%-14s score=%.2f", q.Query, ranked[0].ChunkID, ranked[0].Score)
		if pass != (len(q.Gold) > 0) {
			t.Errorf("%q: score %.2f, gate %.2f, in-domain=%v", q.Query, ranked[0].Score, qualityGate, len(q.Gold) > 0)
		}
	}
}

func liveRetriever(t *testing.T) *retriever {
	t.Helper()
	r := newTestRetriever(t)
	r.emb = embed.New()
	r.vecOnce = sync.Once{}
	r.cache.threshold = ollamaCacheThreshold
	return r
}
