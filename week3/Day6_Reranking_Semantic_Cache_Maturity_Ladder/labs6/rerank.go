package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"

	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/embed"
	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/systemone"
)

// crossEncoder reads query and passage together; systemone (tev1:4b) fits.
type crossEncoder interface {
	Rerank(ctx context.Context, query string, texts []string) ([]systemone.Scored, error)
}

// rerankCandidates scores every candidate against the query and sorts them.
// With a cross-encoder its P(yes) is the score. Offline the score is the
// cosine of the embeddings plus features of the (query, chunk) pair:
// entity codes of the query that the chunk has, codes of the same kind that
// contradict the query (T-1 for a question about T-2), and the question type.
func (r *retriever) rerankCandidates(ctx context.Context, query string, cands []Candidate) ([]Candidate, error) {
	if len(cands) == 0 {
		return nil, nil
	}
	texts := make([]string, len(cands))
	for i, c := range cands {
		texts[i] = c.Text
	}
	scores := make([]float64, len(cands))
	if r.reranker != nil {
		ranked, err := r.reranker.Rerank(ctx, query, texts)
		if err != nil {
			return nil, fmt.Errorf("rerank: %w", err)
		}
		for _, s := range ranked {
			scores[s.Index] = s.Score
		}
	} else {
		cos, err := r.cosines(ctx, query, cands)
		if err != nil {
			return nil, err
		}
		for i := range cands {
			scores[i] = 0.5*cos[i] + pairFeatures(query, texts[i])
		}
	}
	out := slices.Clone(cands)
	for i := range out {
		out[i].Score = scores[i]
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}

// cosines scores candidates by the cosine of the query and chunk vectors.
// Chunks of the corpus reuse the index vectors, so a query costs one
// embedding call; unknown texts are embedded on the fly.
func (r *retriever) cosines(ctx context.Context, query string, cands []Candidate) ([]float64, error) {
	vecs, err := r.chunkVectors(ctx)
	if err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}
	q, err := embedOne(ctx, r.emb, query)
	if err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}
	out := make([]float64, len(cands))
	for i, c := range cands {
		j, ok := r.vecIndex[c.ChunkID]
		if ok && r.normalized()[j].Text == c.Text {
			out[i] = embed.Cosine(q, vecs[j])
			continue
		}
		v, err := embedOne(ctx, r.emb, c.Text)
		if err != nil {
			return nil, fmt.Errorf("rerank: %w", err)
		}
		out[i] = embed.Cosine(q, v)
	}
	return out, nil
}

// pairFeatures is the offline part of the re-ranker score.
func pairFeatures(query, text string) float64 {
	score := 0.0
	var tc []string
	for _, c := range codeRE.FindAllString(text, -1) {
		tc = append(tc, strings.ToUpper(c))
	}
	for _, q := range strings.Split(queryCodes(query), ",") {
		switch {
		case q == "":
		case slices.Contains(tc, q):
			score += 0.5
		case slices.ContainsFunc(tc, func(c string) bool { return codeKind(c) == codeKind(q) }):
			score -= 0.5 // same kind, other value: T-1 when the query asks about T-2
		}
	}
	score += 0.6 * stemOverlap(query, text)
	low := strings.ToLower(text)
	switch queryIntent(query) {
	case "signer":
		if strings.Contains(low, "підписа") {
			score += 0.3
		}
	case "price":
		if strings.Contains(low, "ставк") {
			score += 0.2
		}
	case "about":
		if c := codeRE.FindStringIndex(text); c != nil && c[0] < 12 && strings.Contains(text, "—") {
			score += 0.2 // a definition: "Тариф T-2 — ..."
		}
	}
	return score
}

// stemOverlap is the share of content words of the query (4+ letters) whose
// first 5 letters occur in the text: a crude stemmer for Ukrainian endings.
func stemOverlap(query, text string) float64 {
	have := map[string]bool{}
	for _, w := range tokens(text) {
		have[stem(w)] = true
	}
	total, hit := 0, 0
	for _, w := range tokens(query) {
		if utf8.RuneCountInString(w) < 4 || codeRE.MatchString(w) {
			continue
		}
		total++
		if have[stem(w)] {
			hit++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(hit) / float64(total)
}

func stem(w string) string {
	r := []rune(w)
	return string(r[:min(5, len(r))])
}

// codeKind is the prefix of a code: T, A, CT, F, CC.
func codeKind(code string) string {
	return strings.TrimRight(strings.SplitN(code, "-", 2)[0], "0123456789.")
}

// Evidence is the re-ranked pool with the graph facts, input of format.
type Evidence struct {
	Plan      Plan           `json:"plan"`
	Before    []string       `json:"before"` // pool order after RRF, before rerank
	Top       []Candidate    `json:"top"`
	Facts     []Edge         `json:"facts,omitempty"`
	Steps     int            `json:"steps"`
	Truncated bool           `json:"truncated,omitempty"`
	Branches  map[string]int `json:"branches"`
}

// rrfK is the usual Reciprocal Rank Fusion constant.
const rrfK = 60

// fuse merges the branch rankings with RRF. Scores of BM25 and cosine live
// on different scales, so only ranks are added.
func fuse(results []RetrievalResult) []Candidate {
	byID := map[string]Candidate{}
	rrf := map[string]float64{}
	for _, res := range results {
		for rank, c := range res.Candidates {
			if _, ok := byID[c.ChunkID]; !ok {
				byID[c.ChunkID] = c
			}
			rrf[c.ChunkID] += 1.0 / float64(rrfK+rank+1)
		}
	}
	out := make([]Candidate, 0, len(byID))
	for id, c := range byID {
		c.Score = rrf[id]
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ChunkID < out[j].ChunkID
	})
	return out
}

// branchOrder keeps the join output deterministic: map order is random.
var branchOrder = []string{"vector_search", "keyword_search", "graph_filter", "graph_traversal"}

func decodeJoin(in map[string]any) ([]RetrievalResult, error) {
	var out []RetrievalResult
	for _, name := range branchOrder {
		v, ok := in[name]
		if !ok {
			continue
		}
		var res RetrievalResult
		data, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("join %s: %w", name, err)
		}
		if err := json.Unmarshal(data, &res); err != nil {
			return nil, fmt.Errorf("join %s: %w", name, err)
		}
		out = append(out, res)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("join: no branch results")
	}
	return out, nil
}

// qualityGate is the minimum re-rank score of the top-1 candidate for a
// single-hop answer. Calibrated on the gold set (see eval_test.go, README).
const qualityGate = 0.55

// rerankNode fuses the branches, re-ranks the pool and routes to format when
// the evidence is enough, otherwise to insufficient.
func (r *retriever) rerankNode(ctx agent.Context, in map[string]any) (*session.Event, error) {
	results, err := decodeJoin(in)
	if err != nil {
		return nil, err
	}
	ev := Evidence{Plan: results[0].Plan, Branches: map[string]int{}}
	for _, res := range results {
		ev.Steps += res.Steps
		ev.Facts = append(ev.Facts, res.Facts...)
		ev.Truncated = ev.Truncated || res.Truncated
		ev.Branches[res.Branch] = len(res.Candidates)
	}
	pool := fuse(results)
	pool = pool[:min(poolK, len(pool))]
	for _, c := range pool {
		ev.Before = append(ev.Before, c.ChunkID)
	}
	ranked, err := r.rerankCandidates(ctx, ev.Plan.Query, pool)
	if err != nil {
		return nil, err
	}
	ev.Top = ranked[:min(topN, len(ranked))]

	out := session.NewEvent(ctx, ctx.InvocationID())
	out.Output = ev
	out.Routes = []string{"insufficient"}
	if grounded(ev) {
		out.Routes = []string{"grounded"}
	}
	return out, nil
}

// grounded is the evidence criterion. Multi-hop: the walk finished inside the
// budget and every merchant has a contract and a signer. Others: the top-1
// candidate passes the quality gate.
func grounded(ev Evidence) bool {
	if ev.Steps > ev.Plan.Budget || ev.Truncated {
		return false
	}
	if ev.Plan.Traverse {
		rows := signerRows(ev.Facts)
		if len(rows) == 0 {
			return false
		}
		for _, row := range rows {
			if row.signer == nil {
				return false
			}
		}
		return true
	}
	if ev.Plan.UseFilter {
		return len(ev.Facts) > 0
	}
	return len(ev.Top) > 0 && ev.Top[0].Score >= qualityGate
}
