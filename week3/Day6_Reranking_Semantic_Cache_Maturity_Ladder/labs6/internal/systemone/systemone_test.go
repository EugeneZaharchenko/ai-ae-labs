package systemone

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// roundTripFunc serves requests in-process: no port, so the tests also run
// where binding a socket is not allowed.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		handler(rec, r)
		return rec.Result(), nil
	})
	return &Client{BaseURL: "http://d1.test", HTTP: &http.Client{Transport: rt}}
}

// request is what the client sends, decoded for assertions.
type request struct {
	Model     string            `json:"model"`
	State     map[string]string `json:"state"`
	Questions map[string]struct {
		Type         string `json:"type"`
		Instructions string `json:"instructions"`
	} `json:"questions"`
}

func TestRerank_OrdersByPYes(t *testing.T) {
	t.Parallel()
	// P(yes) by passage: the T-2 passage is relevant, the rest are not.
	pyes := map[string]float64{"T-1 rate": 0.2, "T-2 rate": 0.9, "unrelated": 0.05, "T-2 again": 0.9}
	c := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if req.Model != DefaultModel || req.State["query"] != "ставка T-2" {
			t.Errorf("request = %+v", req)
		}
		if q := req.Questions["relevant"]; q.Type != "noul" || q.Instructions == "" {
			t.Errorf("question = %+v, want a noul", q)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answers": map[string]any{"relevant": map[string]float64{"noul": pyes[req.State["passage"]]}},
			"usage":   map[string]int{"input_tokens": 10, "output_tokens": 0},
		})
	})
	got, err := c.Rerank(context.Background(), "ставка T-2", []string{"T-1 rate", "T-2 rate", "unrelated", "T-2 again"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	want := []int{1, 3, 0, 2} // ties (1, 3) keep input order
	for i, s := range got {
		if s.Index != want[i] {
			t.Fatalf("order = %+v, want indexes %v", got, want)
		}
	}
	if got[0].Score != 0.9 {
		t.Errorf("top score = %f, want 0.9", got[0].Score)
	}
}

func TestRerank_Empty(t *testing.T) {
	t.Parallel()
	got, err := (&Client{BaseURL: "http://unused"}).Rerank(context.Background(), "q", nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want empty, nil", got, err)
	}
}

func TestRerank_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"unsupported model", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"unsupported decision encoding \"lfm2-d1\""}`))
		}, "HTTP 400"},
		{"not json", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<html>`))
		}, "bad response"},
		{"missing answer", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"answers":{}}`))
		}, "no answers.relevant.noul"},
		{"answer without noul", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"answers":{"relevant":{"choice":"x"}}}`))
		}, "no answers.relevant.noul"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := fakeServer(t, tt.handler).Rerank(context.Background(), "q", []string{"a"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestRerank_TransportErrors(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Client{BaseURL: "http://127.0.0.1:1"}).Rerank(ctx, "q", []string{"a"}); err == nil {
		t.Error("want an error for a cancelled context")
	}
	if _, err := (&Client{BaseURL: "://bad"}).Rerank(context.Background(), "q", []string{"a"}); err == nil {
		t.Error("want an error for a malformed base URL")
	}
}

// TestNew uses t.Setenv, so it cannot run in parallel.
func TestNew(t *testing.T) {
	t.Setenv("SYSTEMONE_URL", "")
	t.Setenv("SYSTEMONE_MODEL", "")
	c := New()
	if c != nil {
		t.Fatalf("New() = %+v without SYSTEMONE_URL, want nil", c)
	}
	if !strings.HasPrefix(c.Name(), "off") {
		t.Errorf("nil Name = %q", c.Name())
	}

	t.Setenv("SYSTEMONE_URL", "http://localhost:11434/")
	c = New()
	if c == nil || c.BaseURL != "http://localhost:11434" {
		t.Fatalf("New() = %+v", c)
	}
	if c.Name() != DefaultModel+" @ http://localhost:11434" {
		t.Errorf("Name = %q", c.Name())
	}

	t.Setenv("SYSTEMONE_MODEL", "d1-omni")
	if got := New().Name(); !strings.HasPrefix(got, "d1-omni @") {
		t.Errorf("Name = %q, want the SYSTEMONE_MODEL override", got)
	}
}

// TestWireFormat pins the bytes the model reads: Cyrillic as UTF-8 (escaped
// \uXXXX scored 0.867 where UTF-8 scored 0.963) and query before passage.
func TestWireFormat(t *testing.T) {
	t.Parallel()
	var body string
	c := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = w.Write([]byte(`{"answers":{"relevant":{"noul":0.5}}}`))
	})
	if _, err := c.Rerank(context.Background(), "Яка ставка T-2?", []string{"Тариф T-2"}); err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if !strings.Contains(body, "Яка ставка T-2?") || strings.Contains(body, `\u`) {
		t.Errorf("Cyrillic is not sent as UTF-8: %s", body)
	}
	if q, p := strings.Index(body, `"query"`), strings.Index(body, `"passage"`); q < 0 || p < q {
		t.Errorf("want query before passage: %s", body)
	}
}

func TestSame(t *testing.T) {
	t.Parallel()
	c := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			State     map[string]string `json:"state"`
			Questions map[string]struct {
				Type string `json:"type"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if req.State["question_a"] != "Що таке T-2?" || req.State["question_b"] != "Розкажи про T-2" {
			t.Errorf("state = %v", req.State)
		}
		if req.Questions["same"].Type != "noul" {
			t.Errorf("questions = %+v, want a noul named same", req.Questions)
		}
		_, _ = w.Write([]byte(`{"answers":{"same":{"type":"noul","noul":0.89}}}`))
	})
	got, err := c.Same(context.Background(), "Що таке T-2?", "Розкажи про T-2")
	if err != nil || got != 0.89 {
		t.Fatalf("Same = %v, %v; want 0.89, nil", got, err)
	}
}

func TestSame_Error(t *testing.T) {
	t.Parallel()
	c := fakeServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"relevant":{"noul":0.5}}}`)) // wrong name
	})
	if _, err := c.Same(context.Background(), "a", "b"); err == nil || !strings.Contains(err.Error(), "answers.same.noul") {
		t.Fatalf("err = %v, want a missing answers.same.noul", err)
	}
}
