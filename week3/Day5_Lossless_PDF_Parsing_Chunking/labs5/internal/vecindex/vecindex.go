// Package vecindex is an in-memory vector index with a deterministic stub
// embedding.
//
// Say it in the README exactly like this: "deterministic stub embedding".
// Embed is a hashing-trick bag of words, not a semantic model — it does not
// know that "vendor" and "supplier" are close. It exists so that the lab and
// its tests run without an API key, network or GPU. Replacing it with a real
// embedding model changes the ranking; the swap point is one function.
//
// The index is safe for concurrent use, so ++ A3 (parallel documents) can share
// one instance.
package vecindex

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"slices"
	"strings"
	"sync"
	"unicode"
)

// Dim is the embedding dimension.
const Dim = 128

// Embed returns the L2-normalised hashed bag of words of text. Tokenisation is
// Unicode-aware, so Cyrillic words stay whole.
func Embed(text string) []float64 {
	vec := make([]float64, Dim)
	for _, tok := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tok))
		vec[h.Sum32()%Dim]++
	}
	var norm float64
	for _, v := range vec {
		norm += v * v
	}
	if norm == 0 {
		return vec
	}
	norm = math.Sqrt(norm)
	for i := range vec {
		vec[i] /= norm
	}
	return vec
}

// Hit is one search result.
type Hit struct {
	ID    string  `json:"id"`
	Score float64 `json:"score"`
}

// Index stores one vector per ID. The zero value is ready to use.
type Index struct {
	mu   sync.RWMutex
	ids  []string
	vecs [][]float64
	seen map[string]bool
}

// New returns an empty index.
func New() *Index { return &Index{} }

// Add embeds text under id. An empty or repeated id is an error: a silent
// overwrite would hide a double-indexing bug in the pipeline.
func (x *Index) Add(id, text string) error {
	if id == "" {
		return errors.New("vecindex: empty id")
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.seen == nil {
		x.seen = map[string]bool{}
	}
	if x.seen[id] {
		return fmt.Errorf("vecindex: id %q already indexed", id)
	}
	x.seen[id] = true
	x.ids = append(x.ids, id)
	x.vecs = append(x.vecs, Embed(text))
	return nil
}

// Len is the number of indexed vectors.
func (x *Index) Len() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return len(x.ids)
}

// Search returns the top k hits by cosine similarity, best first; ties are
// broken by id so results are deterministic. k <= 0 returns every hit.
func (x *Index) Search(query string, k int) []Hit {
	q := Embed(query)
	x.mu.RLock()
	hits := make([]Hit, len(x.ids))
	for i, v := range x.vecs {
		var dot float64
		for j := range q {
			dot += q[j] * v[j]
		}
		hits[i] = Hit{ID: x.ids[i], Score: dot}
	}
	x.mu.RUnlock()
	slices.SortStableFunc(hits, func(a, b Hit) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	if k > 0 && k < len(hits) {
		hits = hits[:k]
	}
	return hits
}
