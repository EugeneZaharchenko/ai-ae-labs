package main

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/corpus"
	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/embed"
)

// RetrievalResult is what every retriever branch sends to the JoinNode.
type RetrievalResult struct {
	Branch     string      `json:"branch"`
	Plan       Plan        `json:"plan"`
	Candidates []Candidate `json:"candidates,omitempty"`
	Facts      []Edge      `json:"facts,omitempty"`
	Steps      int         `json:"steps"`
	Skipped    bool        `json:"skipped,omitempty"`
	Truncated  bool        `json:"truncated,omitempty"`
}

// poolK is the first-stage depth: each text retriever returns up to poolK
// candidates, the re-ranker cuts the pool down to topN.
const (
	poolK = 20
	topN  = 3
)

func (r *retriever) normalized() []corpus.Chunk {
	return slices.DeleteFunc(slices.Clone(r.chunks), func(c corpus.Chunk) bool { return c.Kind == "heading" })
}

func candidateOf(c corpus.Chunk, score float64) Candidate {
	return Candidate{ChunkID: c.ID, DocumentID: c.DocumentID, SectionPath: c.SectionPath, Text: c.Text, Score: score}
}

// chunkVectors embeds the corpus once; the vectors are reused by every query.
func (r *retriever) chunkVectors(ctx context.Context) ([][]float32, error) {
	r.vecOnce.Do(func() {
		cs := r.normalized()
		texts := make([]string, len(cs))
		for i, c := range cs {
			texts[i] = c.Text
		}
		r.vecs, r.vecErr = r.emb.Embed(ctx, texts)
	})
	return r.vecs, r.vecErr
}

// vectorTopK is plain dense retrieval: cosine between query and chunk vectors.
func (r *retriever) vectorTopK(ctx context.Context, query string, k int) ([]Candidate, error) {
	vecs, err := r.chunkVectors(ctx)
	if err != nil {
		return nil, fmt.Errorf("vector index: %w", err)
	}
	q, err := embedOne(ctx, r.emb, query)
	if err != nil {
		return nil, fmt.Errorf("vector query: %w", err)
	}
	cs := r.normalized()
	out := make([]Candidate, len(cs))
	for i, c := range cs {
		out[i] = candidateOf(c, embed.Cosine(q, vecs[i]))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out[:min(k, len(out))], nil
}

func (r *retriever) vectorSearch(ctx agent.Context, p Plan) (RetrievalResult, error) {
	cands, err := r.vectorTopK(ctx, p.TextQuery, poolK)
	if err != nil {
		return RetrievalResult{}, err
	}
	return RetrievalResult{Branch: "vector_search", Plan: p, Candidates: cands, Steps: 1}, nil
}

func (r *retriever) keywordSearch(_ agent.Context, p Plan) (RetrievalResult, error) {
	// Shares the text sub-query with vector_search, so it costs no extra step.
	return RetrievalResult{Branch: "keyword_search", Plan: p, Candidates: r.bm25.search(p.TextQuery, poolK)}, nil
}

// graphFilter answers the structured part: merchants on the tariffs of the
// query, and whether the NBU 2026 requirement covers those tariffs.
func (r *retriever) graphFilter(_ agent.Context, p Plan) (RetrievalResult, error) {
	res := RetrievalResult{Branch: "graph_filter", Plan: p}
	if !p.UseFilter {
		res.Skipped = true
		return res, nil
	}
	res.Steps = 1
	g := r.graph
	tariffs := p.Filter.Tariffs
	if len(tariffs) == 0 && p.Filter.NBU && len(p.Filter.Merchants) == 0 {
		for _, e := range g.prev(nbu2026, "subject_to") {
			tariffs = append(tariffs, e.From)
		}
	}
	for _, t := range tariffs {
		if p.Filter.NBU {
			for _, e := range g.next(t, "subject_to") {
				res.Facts = append(res.Facts, e)
				res.Candidates = append(res.Candidates, g.candidate(e.Source, 1))
			}
		}
		for _, e := range g.prev(t, "on_tariff") {
			res.Facts = append(res.Facts, e)
			res.Candidates = append(res.Candidates, g.candidate(e.Source, 1))
		}
	}
	for _, m := range p.Filter.Merchants {
		for _, e := range g.next(m, "on_tariff") {
			res.Facts = append(res.Facts, e)
			res.Candidates = append(res.Candidates, g.candidate(e.Source, 1))
		}
	}
	if p.Filter.NBU && len(p.Filter.Merchants) > 0 {
		for _, e := range g.prev(nbu2026, "subject_to") {
			res.Facts = append(res.Facts, e)
			res.Candidates = append(res.Candidates, g.candidate(e.Source, 1))
		}
	}
	return res, nil
}

// graphTraversal walks Tariff → Merchant → Contract → Signer. Every hop is a
// step; when the budget runs out the walk stops and the result is truncated.
func (r *retriever) graphTraversal(_ agent.Context, p Plan) (RetrievalResult, error) {
	res := RetrievalResult{Branch: "graph_traversal", Plan: p}
	if !p.Traverse {
		res.Skipped = true
		return res, nil
	}
	hops := p.Budget - 1 // the text search step
	if p.UseFilter {
		hops--
	}
	g := r.graph
	merchants := p.Filter.Merchants
	contracts := p.Filter.Contracts
	hop := func() bool {
		if res.Steps >= hops {
			res.Truncated = true
			return false
		}
		res.Steps++
		return true
	}
	if len(merchants) == 0 && len(contracts) == 0 && len(p.Filter.Tariffs) > 0 {
		if !hop() {
			return res, nil
		}
		for _, t := range p.Filter.Tariffs {
			for _, e := range g.prev(t, "on_tariff") {
				merchants = append(merchants, e.From)
				res.Facts = append(res.Facts, e)
			}
		}
	}
	if len(merchants) > 0 {
		if !hop() {
			return res, nil
		}
		for _, m := range merchants {
			for _, e := range g.next(m, "has_contract") {
				contracts = append(contracts, e.To)
				res.Facts = append(res.Facts, e)
				res.Candidates = append(res.Candidates, g.candidate(e.Source, 1))
			}
		}
	}
	if len(contracts) > 0 {
		if !hop() {
			return res, nil
		}
		for _, c := range contracts {
			for _, e := range g.next(c, "signed_by") {
				res.Facts = append(res.Facts, e)
				res.Candidates = append(res.Candidates, g.candidate(e.Source, 1))
			}
		}
	}
	return res, nil
}

// bm25Index is a classic Okapi BM25 over the chunk tokens.
type bm25Index struct {
	docs  []corpus.Chunk
	terms [][]string
	df    map[string]int
	avgdl float64
}

const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

func newBM25(chunks []corpus.Chunk) *bm25Index {
	idx := &bm25Index{df: map[string]int{}}
	total := 0
	for _, c := range chunks {
		if c.Kind == "heading" {
			continue
		}
		ts := tokens(c.Text)
		idx.docs = append(idx.docs, c)
		idx.terms = append(idx.terms, ts)
		total += len(ts)
		seen := map[string]bool{}
		for _, t := range ts {
			if !seen[t] {
				idx.df[t]++
				seen[t] = true
			}
		}
	}
	if len(idx.docs) > 0 {
		idx.avgdl = float64(total) / float64(len(idx.docs))
	}
	return idx
}

func (idx *bm25Index) search(query string, k int) []Candidate {
	n := float64(len(idx.docs))
	var out []Candidate
	for i, ts := range idx.terms {
		tf := map[string]int{}
		for _, t := range ts {
			tf[t]++
		}
		score := 0.0
		for _, q := range slices.Compact(slices.Sorted(slices.Values(tokens(query)))) {
			f := float64(tf[q])
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (n-float64(idx.df[q])+0.5)/(float64(idx.df[q])+0.5))
			score += idf * f * (bm25K1 + 1) / (f + bm25K1*(1-bm25B+bm25B*float64(len(ts))/idx.avgdl))
		}
		if score > 0 {
			out = append(out, candidateOf(idx.docs[i], score))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out[:min(k, len(out))]
}
