package embed

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStub_DeterministicUnitVectors(t *testing.T) {
	t.Parallel()
	var c Client
	texts := []string{"Тариф T-2 — ставка 2.9 %", "Тариф T-2 — ставка 2.9 %", "", "!!!"}
	got, err := c.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != len(texts) {
		t.Fatalf("got %d vectors, want %d", len(got), len(texts))
	}
	if len(got[0]) != stubDim {
		t.Fatalf("dim = %d, want %d", len(got[0]), stubDim)
	}
	if s := Cosine(got[0], got[1]); math.Abs(s-1) > 1e-6 {
		t.Errorf("same text: cosine = %f, want 1", s)
	}
	var norm float64
	for _, x := range got[0] {
		norm += float64(x) * float64(x)
	}
	if math.Abs(norm-1) > 1e-6 {
		t.Errorf("norm² = %f, want 1", norm)
	}
	for _, i := range []int{2, 3} {
		if s := Cosine(got[i], got[0]); s != 0 {
			t.Errorf("text %q without words: cosine = %f, want 0", texts[i], s)
		}
	}
}

func TestEmbed_EmptyInput(t *testing.T) {
	t.Parallel()
	// A Client pointing nowhere proves no call is made for an empty batch.
	c := Client{BaseURL: "http://127.0.0.1:0"}
	got, err := c.Embed(context.Background(), nil)
	if err != nil || got != nil {
		t.Fatalf("Embed(nil) = %v, %v; want nil, nil", got, err)
	}
}

func TestCosine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", []float32{1, 2, 3}, []float32{1, 2, 3}, 1},
		{"scaled", []float32{1, 0}, []float32{5, 0}, 1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1},
		{"length mismatch", []float32{1, 0}, []float32{1, 0, 0}, 0},
		{"empty", nil, nil, 0},
		{"zero vector", []float32{0, 0}, []float32{1, 0}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Cosine(tt.a, tt.b); math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("Cosine = %f, want %f", got, tt.want)
			}
		})
	}
}

func TestNormalizeBase(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"http://localhost:11434", "http://localhost:11434"},
		{"http://localhost:11434/", "http://localhost:11434"},
		{" http://localhost:11434/v1 ", "http://localhost:11434"},
		{"http://localhost:11434/v1/", "http://localhost:11434"},
	}
	for _, tt := range tests {
		if got := normalizeBase(tt.in); got != tt.want {
			t.Errorf("normalizeBase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestNew uses t.Setenv, so it cannot run in parallel.
func TestNew(t *testing.T) {
	tests := []struct {
		name               string
		model, base, key   string
		wantBase, wantName string
	}{
		{name: "nothing set → stub", wantName: "stub"},
		{name: "base URL alone stays stub", base: "http://ollama:11434", wantName: "stub"},
		{name: "model → local default", model: "embeddinggemma", wantBase: DefaultBaseURL, wantName: "ollama embeddinggemma"},
		{name: "model + base", model: "qwen3-embedding", base: "http://gpu:11434/v1", key: "k", wantBase: "http://gpu:11434", wantName: "ollama qwen3-embedding"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OLLAMA_EMBED_MODEL", tt.model)
			t.Setenv("OLLAMA_BASE_URL", tt.base)
			t.Setenv("OLLAMA_API_KEY", tt.key)
			c := New()
			if c.BaseURL != tt.wantBase {
				t.Errorf("BaseURL = %q, want %q", c.BaseURL, tt.wantBase)
			}
			if tt.model != "" && c.APIKey != tt.key {
				t.Errorf("APIKey = %q, want %q", c.APIKey, tt.key)
			}
			if !strings.HasPrefix(c.Name(), tt.wantName) {
				t.Errorf("Name = %q, want prefix %q", c.Name(), tt.wantName)
			}
		})
	}
}

// roundTripFunc serves requests in-process: no port, so the tests also run
// where binding a socket is not allowed.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeOllama answers /api/embed with handler and returns a Client aimed at it.
func fakeOllama(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		handler(rec, r)
		return rec.Result(), nil
	})
	return &Client{BaseURL: "http://ollama.test", HTTP: &http.Client{Transport: rt}}
}

func TestOllama_Success(t *testing.T) {
	t.Parallel()
	c := fakeOllama(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s, want POST /api/embed", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if req.Model != DefaultModel {
			t.Errorf("model = %q, want %q", req.Model, DefaultModel)
		}
		vecs := make([][]float32, len(req.Input))
		for i := range vecs {
			vecs[i] = []float32{float32(i), 1}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	})
	c.APIKey = "secret"
	got, err := c.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 2 || got[1][0] != 1 {
		t.Fatalf("got %v", got)
	}
}

func TestOllama_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"model not pulled", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"model \"embeddinggemma\" not found"}`))
		}, "ollama pull embeddinggemma"},
		{"not json", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<html>`))
		}, "bad response"},
		{"wrong count", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"embeddings":[[1]]}`))
		}, "1 vectors for 2 texts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := fakeOllama(t, tt.handler)
			_, err := c.Embed(context.Background(), []string{"a", "b"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestOllama_Unreachable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := Client{BaseURL: "http://127.0.0.1:1"}
	if _, err := c.Embed(ctx, []string{"a"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestOllama_BadURL(t *testing.T) {
	t.Parallel()
	c := Client{BaseURL: "://bad"}
	if _, err := c.Embed(context.Background(), []string{"a"}); err == nil {
		t.Fatal("want an error for a malformed base URL")
	}
}

// failingEmbedder lets Rerank's error paths run without a server.
type failingEmbedder struct {
	vecs [][]float32
	err  error
}

func (f failingEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return f.vecs, f.err
}

func TestRerank(t *testing.T) {
	t.Parallel()
	texts := []string{
		"Знахідка F-104: зміни розгорталися без затвердження.",
		"Тариф T-2 — тарифний план зі ставкою комісії 2.9 %.",
		"Тариф T-2 — тарифний план зі ставкою комісії 2.9 %.",
	}
	got, err := Rerank(context.Background(), &Client{}, "тариф T-2 ставка", texts)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}
	// Equal texts tie; the stable sort keeps their input order.
	if got[0].Index != 1 || got[1].Index != 2 || got[2].Index != 0 {
		t.Errorf("order = %v, want indexes 1, 2, 0", got)
	}
	if got[0].Score <= got[2].Score {
		t.Errorf("relevant score %f not above unrelated %f", got[0].Score, got[2].Score)
	}
}

func TestRerank_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if got, err := Rerank(ctx, failingEmbedder{}, "q", nil); got != nil || err != nil {
		t.Errorf("no texts: got %v, %v; want nil, nil", got, err)
	}
	boom := errors.New("boom")
	if _, err := Rerank(ctx, failingEmbedder{err: boom}, "q", []string{"a"}); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
	if _, err := Rerank(ctx, failingEmbedder{vecs: [][]float32{{1}}}, "q", []string{"a"}); err == nil {
		t.Error("want an error when the embedder returns too few vectors")
	}
}
