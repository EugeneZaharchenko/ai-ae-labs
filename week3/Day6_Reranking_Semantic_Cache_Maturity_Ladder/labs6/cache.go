package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/corpus"
	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/embed"
)

// cacheScope isolates cache entries: an answer is reused only inside the same
// tenant, the same permission policy and the same corpus version.
type cacheScope struct {
	Tenant        string
	Policy        string
	CorpusVersion string
}

type cacheEntry struct {
	scope   cacheScope
	query   string
	codes   string // sorted entity codes from the query: T-2, CT-2025-031, A-114 ...
	intent  string
	vec     []float32
	answer  string
	expires time.Time
}

// semanticCache stores final answers. A lookup hits when scope, entity codes
// and intent are equal and the query embeddings are close enough. Cosine alone
// is not enough: "тариф T-2" and "тариф T-1" are almost the same string.
type semanticCache struct {
	mu        sync.Mutex
	entries   []cacheEntry
	threshold float64
	ttl       time.Duration
	now       func() time.Time
	stats     cacheStats
}

type cacheStats struct {
	Exact, Semantic, Miss int
}

const (
	defaultCacheThreshold = 0.40 // calibrated on the stub embedder, see README
	defaultCacheTTL       = 24 * time.Hour
)

func newSemanticCache(threshold float64, ttl time.Duration) *semanticCache {
	return &semanticCache{threshold: threshold, ttl: ttl, now: time.Now}
}

// lookup returns the cached answer and whether it was an exact or semantic hit.
func (c *semanticCache) lookup(ctx context.Context, e embedder, s cacheScope, query string) (string, string, error) {
	norm := normalize(query)
	codes, intent := queryCodes(query), queryIntent(query)
	vec, err := embedOne(ctx, e, norm)
	if err != nil {
		return "", "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	best, bestScore := -1, -1.0
	for i, en := range c.entries {
		if en.scope != s || now.After(en.expires) || en.codes != codes || en.intent != intent {
			continue
		}
		if en.query == norm {
			c.stats.Exact++
			return en.answer, "exact", nil
		}
		if sc := embed.Cosine(vec, en.vec); sc > bestScore {
			best, bestScore = i, sc
		}
	}
	if best >= 0 && bestScore >= c.threshold {
		c.stats.Semantic++
		return c.entries[best].answer, "semantic", nil
	}
	c.stats.Miss++
	return "", "", nil
}

func (c *semanticCache) store(ctx context.Context, e embedder, s cacheScope, query, answer string) error {
	norm := normalize(query)
	vec, err := embedOne(ctx, e, norm)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = slices.DeleteFunc(c.entries, func(en cacheEntry) bool { return en.scope == s && en.query == norm })
	c.entries = append(c.entries, cacheEntry{
		scope: s, query: norm, codes: queryCodes(query), intent: queryIntent(query),
		vec: vec, answer: answer, expires: c.now().Add(c.ttl),
	})
	return nil
}

// invalidate drops every entry built on another corpus version.
func (c *semanticCache) invalidate(version string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = slices.DeleteFunc(c.entries, func(en cacheEntry) bool { return en.scope.CorpusVersion != version })
}

func (c *semanticCache) snapshot() cacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

func embedOne(ctx context.Context, e embedder, text string) ([]float32, error) {
	v, err := e.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return v[0], nil
}

var codeRE = regexp.MustCompile(`(?i)\b(?:CT-\d{4}-\d{3}|[A-Z]{1,2}-\d+|CC\d+\.\d+)\b`)

// queryCodes returns the sorted, upper-cased entity codes of a query.
func queryCodes(q string) string {
	found := codeRE.FindAllString(q, -1)
	for i := range found {
		found[i] = strings.ToUpper(found[i])
	}
	slices.Sort(found)
	return strings.Join(slices.Compact(found), ",")
}

// queryIntent is a coarse question type. Two queries with the same codes but a
// different intent ("ставка T-2" vs "мерчанти на T-2") must not share an answer.
func queryIntent(q string) string {
	q = strings.ToLower(q)
	switch {
	case strings.Contains(q, "підпис"):
		return "signer"
	case containsAny(q, "скільки", "ціна", "ціну", "коштує", "ставк", "комісі"):
		return "price"
	case containsAny(q, "мерчант"):
		return "merchants"
	case containsAny(q, "знахідк", "finding", "severity"):
		return "findings"
	default:
		return "about"
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// corpusVersion is a short hash of the corpus content: a new chunks.json gives
// a new version, so old cache entries stop matching.
func corpusVersion(chunks []corpus.Chunk) string {
	h := sha256.New()
	for _, c := range chunks {
		h.Write([]byte(c.ID + "\x00" + c.Text + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
