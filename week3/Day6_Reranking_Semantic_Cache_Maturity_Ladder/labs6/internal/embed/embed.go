// Package embed turns text into vectors and ranks texts by how close they are
// to a query.
//
// One type, two backends:
//
//   - pure Go by default: the zero Client is a deterministic offline stub
//     (hashed character trigrams). No key, no network, no Ollama — go test and
//     go run work anywhere;
//   - Ollama on request: set OLLAMA_EMBED_MODEL (e.g. embeddinggemma) and New
//     calls POST /api/embed on OLLAMA_BASE_URL (default DefaultBaseURL).
//     Pull the model first: ollama pull embeddinggemma.
//     API: https://docs.ollama.com/capabilities/embeddings
//
// Ollama is opt-in through the model variable, not through OLLAMA_BASE_URL:
// apps/.env sets that URL for chat models, and a chat-only setup must not
// silently switch the lab to a model that was never pulled.
//
// The stub is for wiring and tests, NOT for calibrating thresholds. It scores
// "тариф T-1" close to "тариф T-2" because the two strings share most of their
// trigrams, and it scores a real paraphrase low because the words differ. A
// cache threshold or a quality gate tuned on the stub means nothing on a real
// model — calibrate on Ollama.
//
// It lives outside the lab package because the homework is the cache and the
// re-rank decisions, not an HTTP client.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dimetron/ai-eng-course/labs/internal/adkenv"
)

// DefaultModel is the Ollama model a Client with BaseURL but no Model uses.
// Multilingual (Ukrainian is in its training set), and the first model the
// Ollama embeddings docs recommend.
const DefaultModel = "embeddinggemma"

// DefaultBaseURL is the local Ollama server, used when OLLAMA_EMBED_MODEL is
// set and OLLAMA_BASE_URL is not.
const DefaultBaseURL = "http://localhost:11434"

// stubDim is the stub vector size: big enough that unrelated trigrams rarely
// collide on a corpus of this size.
const stubDim = 256

// Embedder turns texts into vectors, one per text, in input order.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Client embeds texts. The zero value is the offline stub.
type Client struct {
	// BaseURL of the Ollama server, e.g. http://localhost:11434.
	// Empty means the offline stub.
	BaseURL string
	// Model is the Ollama embedding model; empty means DefaultModel.
	Model string
	// APIKey is sent as a bearer token when set (Ollama Cloud).
	APIKey string
	// HTTP is the client for Ollama calls; nil means http.DefaultClient.
	HTTP *http.Client
}

// New builds a Client from the environment: Ollama when OLLAMA_EMBED_MODEL is
// set, otherwise the pure-Go stub. Print Name() so the reader sees which one
// runs — a silent stub would make every similarity number look plausible.
func New() *Client {
	model, ok := adkenv.Key("OLLAMA_EMBED_MODEL")
	if !ok {
		return &Client{}
	}
	base, ok := adkenv.Key("OLLAMA_BASE_URL")
	if !ok {
		base = DefaultBaseURL
	}
	key, _ := adkenv.Key("OLLAMA_API_KEY")
	return &Client{BaseURL: normalizeBase(base), Model: model, APIKey: key}
}

// Name says which backend runs, for logs and README tables.
func (c *Client) Name() string {
	if c.BaseURL == "" {
		return "stub (pure Go, hashed trigrams — not for threshold calibration; set OLLAMA_EMBED_MODEL for Ollama)"
	}
	return fmt.Sprintf("ollama %s @ %s", c.model(), c.BaseURL)
}

// Embed returns one vector per text, in input order.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if c.BaseURL == "" {
		out := make([][]float32, len(texts))
		for i, t := range texts {
			out[i] = stubVector(t)
		}
		return out, nil
	}
	return c.ollama(ctx, texts)
}

func (c *Client) model() string {
	if c.Model == "" {
		return DefaultModel
	}
	return c.Model
}

// ollama calls POST /api/embed — the native endpoint, which takes a batch and
// returns L2-normalised vectors, one per input, in input order.
func (c *Client) ollama(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"model": c.model(), "input": texts})
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: ollama %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()

	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
		Error      string      `json:"error"`
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("embed: ollama: %w", err)
	}
	if err := json.Unmarshal(data, &out); err != nil && resp.StatusCode == http.StatusOK {
		return nil, fmt.Errorf("embed: ollama: bad response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// The usual cause is a model that was never pulled; name it.
		return nil, fmt.Errorf("embed: ollama %s model %q: HTTP %d: %s (did you run `ollama pull %s`?)",
			c.BaseURL, c.model(), resp.StatusCode, out.Error, c.model())
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embed: ollama returned %d vectors for %d texts", len(out.Embeddings), len(texts))
	}
	return out.Embeddings, nil
}

// normalizeBase accepts the forms OLLAMA_BASE_URL takes in apps/.env —
// with or without a trailing slash or the OpenAI-compatible /v1 suffix —
// and returns the server root that /api/embed hangs off.
func normalizeBase(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	base = strings.TrimSuffix(base, "/v1")
	return strings.TrimRight(base, "/")
}

// Cosine is the cosine similarity of a and b, in [-1, 1].
// It returns 0 when the lengths differ or either vector is zero: such a pair
// has no meaningful angle, and 0 cannot pass any sane threshold.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Scored is one text's position in the input and its similarity to the query.
type Scored struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

// Rerank scores every text against the query by cosine similarity of their
// embeddings and returns them best first. Ties keep input order, so a rerank
// never shuffles candidates it cannot tell apart.
//
// This is a bi-encoder rerank: query and texts are embedded separately. A
// cross-encoder (e.g. BAAI/bge-reranker-v2-m3) reads query and text together
// and is usually more precise — swap it in behind the same signature.
func Rerank(ctx context.Context, e Embedder, query string, texts []string) ([]Scored, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	vecs, err := e.Embed(ctx, append([]string{query}, texts...))
	if err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}
	if len(vecs) != len(texts)+1 {
		return nil, fmt.Errorf("rerank: got %d vectors for %d texts", len(vecs), len(texts)+1)
	}
	out := make([]Scored, len(texts))
	for i := range texts {
		out[i] = Scored{Index: i, Score: Cosine(vecs[0], vecs[i+1])}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}

// stubVector hashes the character trigrams of every word into a fixed-size,
// unit-length vector. Deterministic: the same text always gives the same
// vector, on every machine.
func stubVector(text string) []float32 {
	v := make([]float32, stubDim)
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	})
	for _, w := range words {
		runes := []rune(" " + w + " ")
		for i := 0; i+3 <= len(runes); i++ {
			h := fnv.New32a()
			buf := make([]byte, 0, 3*utf8.UTFMax)
			for _, r := range runes[i : i+3] {
				buf = utf8.AppendRune(buf, r)
			}
			h.Write(buf)
			v[h.Sum32()%stubDim]++
		}
	}
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return v
	}
	n := float32(math.Sqrt(norm))
	for i := range v {
		v[i] /= n
	}
	return v
}
