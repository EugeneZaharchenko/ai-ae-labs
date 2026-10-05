package main

import (
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

// topK — скільки кандидатів search віддає в rerank.
const topK = 5

// answerCache — кеш відповідей. Безпечний для одночасних викликів: коли ви
// зробите fan-out, вузли працюватимуть паралельно, а launcher у режимі web
// обслуговує кілька запитів одночасно.
//
// TODO(студент): семантичний кеш. Зараз ключ — нормалізований рядок запиту
// (точний hit). Перейдіть на схожість (ембединги або власна евристика) —
// перефразований запит має влучати. Ключ має ізолювати tenant, версію
// корпусу й TTL (Homework п.4).
type answerCache struct {
	mu sync.Mutex
	m  map[string]string
}

func (c *answerCache) get(query string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[normalize(query)]
	return v, ok
}

func (c *answerCache) put(query, answer string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]string{}
	}
	c.m[normalize(query)] = answer
}

func normalize(q string) string {
	return strings.Join(tokens(q), " ")
}

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

// retriever тримає корпус і кеш; його методи — вузли графа.
type retriever struct {
	chunks []corpus.Chunk
	cache  answerCache
}

func newRetriever(chunks []corpus.Chunk) *retriever {
	return &retriever{chunks: chunks}
}

// search шукає топ-K кандидатів по чанках із ДЗ 5.
//
// Зараз це BASELINE: частка слів запиту, які є в чанку (keyword overlap).
// Він навмисно наївний — на «яка ставка на тарифі T-2?» чанк про T-1 набирає
// майже стільки ж, скільки чанк про T-2.
//
// TODO(студент): векторний пошук (ембединги) або BM25; для ++ Advanced —
// обидва через fan-out + RRF.
func (r *retriever) search(_ agent.Context, query string) (SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return SearchResult{}, fmt.Errorf("порожній запит")
	}
	if answer, ok := r.cache.get(query); ok {
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

// rerank переранжовує кандидатів до топ-N.
// TODO(студент): cross-encoder (напр. BAAI/bge-reranker-v2-m3) або LLM-судія
// («оціни релевантність 0–10»); збережіть порядок «до/після» для README.
func (r *retriever) rerank(_ agent.Context, in SearchResult) (SearchResult, error) {
	return in, nil // заглушка: порядок не змінюється
}

// answer формує фінальну відповідь із топ-кандидата і пише в кеш.
// TODO(студент): складіть відповідь із кількох кандидатів із цитатами
// chunk_id / document_id; для multi-hop — з обох гілок.
func (r *retriever) answer(_ agent.Context, in SearchResult) (string, error) {
	if len(in.Candidates) == 0 {
		return "Нічого не знайдено: у корпусі немає доказів для цього запиту.", nil
	}
	if in.CacheHit {
		return in.Candidates[0].Text, nil
	}
	top := in.Candidates[0]
	result := fmt.Sprintf("[cache_hit=false] Топ-результат (%s, %s): %s", top.ChunkID, top.DocumentID, top.Text)
	r.cache.put(in.Query, strings.Replace(result, "[cache_hit=false]", "[cache_hit=true]", 1))
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
