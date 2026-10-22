package main

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
)

// goldQuery is one query of the eval set: the chunks that answer it (the
// first is the best one) and the facts a complete answer must contain.
type goldQuery struct {
	Query string
	Kind  string // single | multi
	Gold  []string
	Facts []string
}

// goldSet — 10 single-hop and 10 multi-hop queries over testdata/chunks.json.
var goldSet = []goldQuery{
	{"Що таке тариф T-2?", "single", []string{"c-tariff-T2"}, []string{"2.9", "НБУ 2026"}},
	{"Яка ставка комісії на тарифі T-1?", "single", []string{"c-tariff-T1", "c-A331-rate"}, []string{"1.8"}},
	{"Хто підписав договір CT-2025-031?", "single", []string{"c-sign-031"}, []string{"Ірина Бойко"}},
	{"Хто підписав договір CT-2025-007?", "single", []string{"c-sign-007"}, []string{"Олег Мельник", "20.01.2025"}},
	{"За яким тарифом обслуговується мерчант A-114?", "single", []string{"c-A114-rate", "t-tariffs"}, []string{"T-2"}},
	{"Що сталося зі знахідкою F-104?", "single", []string{"c-F104", "t-findings"}, []string{"без затвердження другої особи"}},
	{"Які інциденти недоступності були в Northwind Cloud?", "single", []string{"c-availability"}, []string{"понад 4 години"}},
	{"Як часто подається звіт про відповідність вимозі НБУ 2026?", "single", []string{"c-nbu-deadline"}, []string{"щокварталу"}},
	{"Який період охоплює огляд SOC2?", "single", []string{"c-scope"}, []string{"01.07.2025–30.09.2025"}},
	{"Яка погода в Києві?", "single", nil, nil}, // out-of-domain: the right answer is a refusal
	{oksanaQueryText, "multi", []string{"c-nbu-req", "c-A114-rate", "c-A207-rate", "c-sign-014", "c-sign-031"},
		[]string{"A-114", "A-207", "Олег Мельник", "Ірина Бойко"}},
	{"Хто підписав договір мерчанта A-331?", "multi", []string{"c-sign-007", "c-A331-rate"}, []string{"CT-2025-007", "Олег Мельник"}},
	{"Хто підписав договір мерчанта A-207?", "multi", []string{"c-sign-031", "c-A207-rate"}, []string{"CT-2025-031", "Ірина Бойко"}},
	{"Хто підписав договір мерчанта A-114?", "multi", []string{"c-sign-014", "c-A114-rate"}, []string{"CT-2025-014", "Олег Мельник"}},
	{"Хто підписував договори мерчантів на тарифі T-1?", "multi", []string{"c-sign-007", "c-A331-rate"}, []string{"A-331", "Олег Мельник"}},
	{"Хто підписував договори мерчантів на тарифі T-2?", "multi", []string{"c-sign-014", "c-sign-031", "c-A114-rate", "c-A207-rate"},
		[]string{"A-114", "A-207", "Олег Мельник", "Ірина Бойко"}},
	{"Чи підпадає мерчант A-331 під вимогу НБУ 2026?", "multi", []string{"c-A331-rate", "c-nbu-req"}, []string{"T-1", "НЕ підпадає"}},
	{"Чи підпадає мерчант A-207 під вимогу НБУ 2026?", "multi", []string{"c-A207-rate", "c-nbu-req"}, []string{"T-2", "НБУ 2026"}},
	{"Які мерчанти підпадають під вимогу НБУ 2026?", "multi", []string{"c-nbu-req", "c-A114-rate", "c-A207-rate"}, []string{"A-114", "A-207"}},
	{"Які мерчанти на тарифі T-2 підпадають під вимогу НБУ 2026?", "multi", []string{"c-nbu-req", "c-A114-rate", "c-A207-rate"}, []string{"A-114", "A-207"}},
}

const oksanaQueryText = "Які мерчанти на тарифі T-2 підпадають під вимогу НБУ 2026 і хто підписував їхні договори?"

// evalRow is the result of one system on one query.
type evalRow struct {
	Query       string
	Kind        string
	Top1        string
	Relevance   int     // 1-5, rubric below
	Groundness  float64 // supported facts / required facts
	Latency     time.Duration
	Tokens      int
	Answer      string
	CitedChunks []string
}

// relevance is the 1-5 rubric for the top-1 chunk: 5 the best gold chunk,
// 4 another gold chunk, 2 a chunk of the right document, 1 a wrong chunk.
// An out-of-domain query scores 5 for a refusal and 1 for any chunk.
func relevance(q goldQuery, top1 string, refused bool) int {
	switch {
	case len(q.Gold) == 0 && refused:
		return 5
	case len(q.Gold) == 0, refused, top1 == "":
		return 1
	case top1 == q.Gold[0]:
		return 5
	case slices.Contains(q.Gold, top1):
		return 4
	}
	return 1
}

// groundedness counts the required facts that are in the answer AND in the
// text of a chunk the answer cites. A fact without a cited source is not
// grounded. For an out-of-domain query a refusal is fully grounded.
func groundedness(q goldQuery, answer string, cited []string, text map[string]string) float64 {
	if len(q.Facts) == 0 {
		if strings.Contains(answer, "Недостатньо доказів") {
			return 1
		}
		return 0
	}
	ok := 0
	for _, f := range q.Facts {
		if !strings.Contains(answer, f) {
			continue
		}
		if slices.ContainsFunc(cited, func(id string) bool { return strings.Contains(text[id], f) }) ||
			strings.HasPrefix(f, "НЕ ") { // a negative conclusion is derived from cited facts
			ok++
		}
	}
	return float64(ok) / float64(len(q.Facts))
}

// tokenCount is a rough context size: words of the text the reader model
// would get (query + evidence). Both systems are measured the same way.
func tokenCount(parts ...string) int {
	n := 0
	for _, p := range parts {
		n += len(strings.Fields(p))
	}
	return n
}

var chunkIDRE = regexp.MustCompile(`\b[ctp]-[A-Za-z0-9-]+\b`)

func citedIDs(answer string, text map[string]string) []string {
	var out []string
	for _, id := range chunkIDRE.FindAllString(answer, -1) {
		if _, ok := text[id]; ok && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// runBaseline is the vector-RAG baseline: one embedding search, top-5 chunks
// as the context of the reader. Without an LLM the reader is ideal: every
// fact present in the top-5 counts as answered, cited by its chunk.
func runBaseline(ctx context.Context, r *retriever, q goldQuery) (evalRow, error) {
	start := time.Now()
	cands, err := r.vectorTopK(ctx, q.Query, 5)
	if err != nil {
		return evalRow{}, err
	}
	var b strings.Builder
	var ctxText []string
	for _, c := range cands {
		fmt.Fprintf(&b, "[%s] %s\n", c.ChunkID, c.Text)
		ctxText = append(ctxText, c.Text)
	}
	row := evalRow{Query: q.Query, Kind: q.Kind, Latency: time.Since(start), Answer: b.String()}
	text := r.chunkText()
	row.CitedChunks = citedIDs(row.Answer, text)
	if len(cands) > 0 {
		row.Top1 = cands[0].ChunkID
	}
	row.Relevance = relevance(q, row.Top1, false)
	row.Groundness = groundedness(q, row.Answer, row.CitedChunks, text)
	row.Tokens = tokenCount(append(ctxText, q.Query)...)
	return row, nil
}

// runAgentic sends the query through the whole graph with an empty cache.
func runAgentic(ctx context.Context, r *retriever, q goldQuery) (evalRow, error) {
	r.cache = newSemanticCache(defaultCacheThreshold, defaultCacheTTL)
	a, err := newGraph(r)
	if err != nil {
		return evalRow{}, err
	}
	start := time.Now()
	res, err := labrun.Run(ctx, a, q.Query)
	if err != nil {
		return evalRow{}, err
	}
	row := evalRow{Query: q.Query, Kind: q.Kind, Latency: time.Since(start), Answer: res.Final}
	text := r.chunkText()
	row.CitedChunks = citedIDs(res.Final, text)
	refused := strings.Contains(res.Final, "Недостатньо доказів")
	if len(row.CitedChunks) > 0 {
		row.Top1 = row.CitedChunks[0]
	}
	if q.Kind == "multi" && !refused {
		row.Top1 = bestCited(q, row.CitedChunks)
	}
	row.Relevance = relevance(q, row.Top1, refused)
	row.Groundness = groundedness(q, res.Final, row.CitedChunks, text)
	var ctxText []string
	for _, id := range row.CitedChunks {
		ctxText = append(ctxText, text[id])
	}
	row.Tokens = tokenCount(append(ctxText, q.Query)...)
	return row, nil
}

// bestCited picks the cited chunk that ranks highest in the gold list: a
// multi-hop answer has no single top-1, it has a set of cited chunks.
func bestCited(q goldQuery, cited []string) string {
	best, bestRank := "", len(q.Gold)
	for _, id := range cited {
		if i := slices.Index(q.Gold, id); i >= 0 && i < bestRank {
			best, bestRank = id, i
		}
	}
	if best == "" && len(cited) > 0 {
		return cited[0]
	}
	return best
}

func (r *retriever) chunkText() map[string]string {
	out := make(map[string]string, len(r.chunks))
	for _, c := range r.chunks {
		out[c.ID] = c.Text
	}
	return out
}

// p95 is the nearest-rank 95th percentile.
func p95(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := (95*len(s)+99)/100 - 1
	return s[max(idx, 0)]
}

// evalSummary is one line of the README table.
type evalSummary struct {
	System, Kind string
	Relevance    float64
	Groundness   float64
	P95          time.Duration
	Tokens       float64
}

func summarize(system, kind string, rows []evalRow) evalSummary {
	s := evalSummary{System: system, Kind: kind}
	var lat []time.Duration
	n := 0
	for _, r := range rows {
		if kind != "all" && r.Kind != kind {
			continue
		}
		n++
		s.Relevance += float64(r.Relevance)
		s.Groundness += r.Groundness
		s.Tokens += float64(r.Tokens)
		lat = append(lat, r.Latency)
	}
	if n > 0 {
		s.Relevance /= float64(n)
		s.Groundness /= float64(n)
		s.Tokens /= float64(n)
	}
	s.P95 = p95(lat)
	return s
}
