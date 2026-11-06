package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/corpus"
)

// Candidate — кандидат retrieval з оцінкою і provenance.
type Candidate struct {
	ChunkID     string  `json:"chunk_id"`
	DocumentID  string  `json:"document_id"`
	SectionPath string  `json:"section_path,omitempty"`
	Text        string  `json:"text"`
	Score       float64 `json:"score"`
}

// SearchResult — запит + кандидати, тече між вузлами графа.
type SearchResult struct {
	Query      string      `json:"query"`
	Candidates []Candidate `json:"candidates"`
	CacheHit   bool        `json:"cache_hit"`
}

// topK — скільки кандидатів baseline search віддає в rerank.
const topK = 5

// defaultChunks — корпус стартера: ті самі LEDGERWORKS-документи, що в ДЗ 5,
// уже нарізані у форматі labs5.Chunk. Шлях відносний — запускайте з теки labs6.
const defaultChunks = "testdata/chunks.json"

// chunksPath обирає корпус: змінна CHUNKS (ваш індекс із ДЗ 5) або стартовий.
func chunksPath(env string) string {
	if env != "" {
		return env
	}
	return defaultChunks
}

// embedder перетворює тексти на вектори — по одному на текст, у тому ж порядку.
//
// Реалізація — internal/embed: за замовчуванням чистий Go (детермінований
// стаб, без мережі), Ollama — якщо задано OLLAMA_EMBED_MODEL
// (https://docs.ollama.com/capabilities/embeddings). Там же embed.Cosine і
// embed.Rerank. Стаб — лише для wiring і тестів: пороги калібруйте на Ollama.
type embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// retriever тримає корпус, граф, індекси, кеш і embedder; його методи — вузли графа.
type retriever struct {
	chunks   []corpus.Chunk
	emb      embedder
	graph    *kgraph
	bm25     *bm25Index
	reranker crossEncoder // nil — офлайн-reranker на ознаках
	cache    *semanticCache
	scope    cacheScope

	vecOnce  sync.Once
	vecs     [][]float32
	vecIndex map[string]int
	vecErr   error
}

func newRetriever(chunks []corpus.Chunk, emb embedder) *retriever {
	return &retriever{
		chunks: chunks,
		emb:    emb,
		graph:  buildGraph(chunks),
		bm25:   newBM25(chunks),
		cache:  newSemanticCache(defaultCacheThreshold, defaultCacheTTL),
		scope:  cacheScope{Tenant: "ledgerworks", Policy: "legal", CorpusVersion: corpusVersion(chunks)},
	}
}

// search шукає топ-K кандидатів по чанках із ДЗ 5.
//
// Це BASELINE: частка слів запиту, які є в чанку (keyword overlap). В agentic
// графі його замінюють vector_search і keyword_search (BM25) + RRF.
func (r *retriever) search(ctx agent.Context, query string) (SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return SearchResult{}, fmt.Errorf("порожній запит")
	}
	answer, kind, err := r.cache.lookup(ctx, r.emb, r.scope, query)
	if err != nil {
		return SearchResult{}, fmt.Errorf("cache: %w", err)
	}
	if kind != "" {
		return SearchResult{Query: query, CacheHit: true, Candidates: []Candidate{{ChunkID: "cache", Text: answer, Score: 1}}}, nil
	}
	q := tokens(query)
	var out []Candidate
	for _, c := range r.chunks {
		if c.Kind == "heading" {
			continue
		}
		if s := overlap(q, tokens(c.Text)); s > 0 {
			out = append(out, Candidate{ChunkID: c.ID, DocumentID: c.DocumentID, SectionPath: c.SectionPath, Text: c.Text, Score: s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > topK {
		out = out[:topK]
	}
	return SearchResult{Query: query, Candidates: out}, nil
}

// rerank переранжовує кандидатів: cross-encoder (tev1:4b), якщо він увімкнений,
// інакше офлайн-reranker на ознаках пари «запит ↔ чанк» (див. rerank.go).
func (r *retriever) rerank(ctx agent.Context, in SearchResult) (SearchResult, error) {
	out, err := r.rerankCandidates(ctx, in.Query, in.Candidates)
	if err != nil {
		return SearchResult{}, err
	}
	in.Candidates = out
	return in, nil
}

// answer — відповідь лінійного конвеєра (baseline): топ-кандидат + запис у кеш.
func (r *retriever) answer(ctx agent.Context, in SearchResult) (string, error) {
	if len(in.Candidates) == 0 {
		return "Нічого не знайдено: у корпусі немає доказів для цього запиту.", nil
	}
	if in.CacheHit {
		return in.Candidates[0].Text, nil
	}
	top := in.Candidates[0]
	result := fmt.Sprintf("[cache_hit=false] Топ-результат (%s, %s): %s", top.ChunkID, top.DocumentID, top.Text)
	if err := r.cache.store(ctx, r.emb, r.scope, in.Query, strings.Replace(result, "[cache_hit=false]", "[cache_hit=true]", 1)); err != nil {
		return "", fmt.Errorf("cache: %w", err)
	}
	return result, nil
}

// tokens — нижній регістр, слова з літер/цифр; дефіс лишається всередині
// слова, щоб "T-2" не злився з "T-1".
func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	})
}

// overlap — частка слів запиту, що зустрічаються в тексті чанка.
func overlap(query, text []string) float64 {
	if len(query) == 0 {
		return 0
	}
	in := make(map[string]bool, len(text))
	for _, t := range text {
		in[t] = true
	}
	hits := 0
	for _, q := range query {
		if in[q] {
			hits++
		}
	}
	return float64(hits) / float64(len(query))
}

func normalize(q string) string {
	return strings.Join(tokens(q), " ")
}
