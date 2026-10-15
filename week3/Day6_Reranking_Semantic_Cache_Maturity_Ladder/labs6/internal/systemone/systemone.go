// Package systemone asks a decision model yes/no questions through the
// /v1/systemone endpoint (https://docs.ollama.com/capabilities/decision):
// Rerank for "does this passage answer the query?" and Same for "can one
// answer serve both questions?" — the semantic-cache check.
//
// A decision model is not a chat model: it answers typed questions over a
// state in one forward pass and generates no text. For re-ranking, the state
// is {query, passage} and the one question is a yes/no ("noul") — "does the
// passage answer the query?". The answer is P(yes), which is the score. That
// makes it a cross-encoder: the model reads query and passage together, unlike
// embed.Rerank, which embeds them apart.
//
// Off by default. Set SYSTEMONE_URL to the server root to enable it:
//
//	ollama pull tev1:4b
//	SYSTEMONE_URL=http://localhost:11434   # SYSTEMONE_MODEL overrides tev1:4b
//
// Other decision models in Ollama (10/2026): tev1:0.8b, nimble (9B), laya
// (421M), clef-flash (9B), clef (27B) — https://ollama.com/search?c=decision.
// LiquidAI d1-3B does not run in Ollama 0.40.1 yet ("unsupported decision
// encoding lfm2-d1").
//
// The model reads the state as JSON text, so its exact bytes matter:
//
//   - Cyrillic must go as UTF-8, not \uXXXX escapes. encoding/json already
//     does this; Python's json.dumps does not unless ensure_ascii=False, and
//     escaped Ukrainian scored 0.867 where UTF-8 scored 0.963.
//   - Key order moves scores a little, so the states are structs (fixed field
//     order), not maps (sorted keys).
//
// How well tev1:4b does on Ukrainian is measured by the live eval in
// eval_test.go: go test -run TestEval -v with SYSTEMONE_URL set.
package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/dimetron/ai-eng-course/labs/internal/adkenv"
)

// DefaultModel is the decision model used when SYSTEMONE_MODEL is not set:
// Together AI's tev1, 4B — small enough for a laptop.
const DefaultModel = "tev1:4b"

// The questions, one per job. Each is a noul: the answer is P(yes).
const (
	relevanceQuestion = "Does the passage contain the answer to the query?"
	sameQuestion      = "Do both questions ask for the same information, so one answer serves both?"
)

// rerankState and sameState are the states the model reads, in this field
// order. Structs, not maps: a map would marshal with sorted keys.
type rerankState struct {
	Query   string `json:"query"`
	Passage string `json:"passage"`
}

type sameState struct {
	QuestionA string `json:"question_a"`
	QuestionB string `json:"question_b"`
}

// Client calls /v1/systemone.
type Client struct {
	// BaseURL is the server root, e.g. http://localhost:11434.
	BaseURL string
	// Model is the model name; empty means DefaultModel.
	Model string
	// HTTP is the client; nil means http.DefaultClient.
	HTTP *http.Client
}

// New builds a Client from SYSTEMONE_URL and SYSTEMONE_MODEL. It returns nil
// when SYSTEMONE_URL is not set: the decision model is opt-in, and a nil
// *Client tells the caller to keep its own re-rank.
func New() *Client {
	base, ok := adkenv.Key("SYSTEMONE_URL")
	if !ok {
		return nil
	}
	model, _ := adkenv.Key("SYSTEMONE_MODEL")
	return &Client{BaseURL: strings.TrimRight(base, "/"), Model: model}
}

// Name says which model and server run, for logs.
func (c *Client) Name() string {
	if c == nil {
		return "off (set SYSTEMONE_URL to enable the decision-model re-rank)"
	}
	return fmt.Sprintf("%s @ %s", c.model(), c.BaseURL)
}

func (c *Client) model() string {
	if c.Model == "" {
		return DefaultModel
	}
	return c.Model
}

// Scored is one text's position in the input and its relevance, P(yes).
type Scored struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

// Rerank scores every text against the query and returns them best first.
// Ties keep input order. One request per text: each passage is its own state.
func (c *Client) Rerank(ctx context.Context, query string, texts []string) ([]Scored, error) {
	out := make([]Scored, len(texts))
	for i, t := range texts {
		p, err := c.noul(ctx, rerankState{Query: query, Passage: t}, "relevant", relevanceQuestion)
		if err != nil {
			return nil, fmt.Errorf("systemone rerank text %d: %w", i, err)
		}
		out[i] = Scored{Index: i, Score: p}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}

// Same returns P(one answer serves both questions) — the check a semantic
// cache needs before it reuses an answer. Cosine similarity cannot make it:
// embeddinggemma scores a paraphrase and a question about another tariff the
// same 0.79, while this question separates them.
func (c *Client) Same(ctx context.Context, a, b string) (float64, error) {
	p, err := c.noul(ctx, sameState{QuestionA: a, QuestionB: b}, "same", sameQuestion)
	if err != nil {
		return 0, fmt.Errorf("systemone same: %w", err)
	}
	return p, nil
}

// noul asks one yes/no question, named name, over state and returns P(yes).
func (c *Client) noul(ctx context.Context, state any, name, instructions string) (float64, error) {
	body, err := json.Marshal(map[string]any{
		"model": c.model(),
		"state": state,
		"questions": map[string]any{
			name: map[string]string{"type": "noul", "instructions": instructions},
		},
	})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s model %q: HTTP %d: %s", c.BaseURL, c.model(), resp.StatusCode, strings.TrimSpace(string(data)))
	}
	// {"answers": {name: answer}, "usage": {...}}; a noul answer carries
	// "noul": P(yes) (model card, "Questions and answers").
	var out struct {
		Answers map[string]struct {
			Noul *float64 `json:"noul"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return 0, fmt.Errorf("bad response: %w", err)
	}
	a, ok := out.Answers[name]
	if !ok || a.Noul == nil {
		return 0, fmt.Errorf("no answers.%s.noul in %s", name, strings.TrimSpace(string(data)))
	}
	return *a.Noul, nil
}
