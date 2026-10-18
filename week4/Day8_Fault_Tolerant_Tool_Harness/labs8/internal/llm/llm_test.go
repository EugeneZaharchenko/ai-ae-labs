package llm

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

type recorder struct {
	calls []string
	err   error
}

func (r *recorder) RecordLLMCall(_ context.Context, m string) error {
	r.calls = append(r.calls, m)
	return r.err
}

// echo reports which req.Model it received.
type echo struct{ seen string }

func (e *echo) Name() string { return "real-model" }
func (e *echo) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	e.seen = req.Model
	return func(yield func(*model.LLMResponse, error) bool) {
		for range 2 {
			if !yield(&model.LLMResponse{}, nil) {
				return
			}
		}
	}
}

func TestCountingRecordsAndRewritesModel(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	inner := &echo{}
	c := Counting{Inner: inner, Recorder: rec}
	req := &model.LLMRequest{Model: "ledger-model"}
	var n int
	for _, err := range c.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 2 || c.Name() != "real-model" || inner.seen != "real-model" || req.Model != "ledger-model" || len(rec.calls) != 1 {
		t.Errorf("n=%d seen=%q caller req=%q calls=%v", n, inner.seen, req.Model, rec.calls)
	}
	// A consumer that stops early must not be yielded to again.
	for range c.GenerateContent(context.Background(), req, false) {
		break
	}
}

func TestCountingFailsWhenItCannotRecord(t *testing.T) {
	t.Parallel()
	c := Counting{Inner: &echo{}, Recorder: &recorder{err: errors.New("disk full")}}
	for _, err := range c.GenerateContent(context.Background(), &model.LLMRequest{}, false) {
		if err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("err = %v", err)
		}
	}
}

func user(text string) *genai.Content { return genai.NewContentFromText(text, genai.RoleUser) }

func toolResponse(name string, data map[string]any) *genai.Content {
	return &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: name, Response: data}}}}
}

func step(t *testing.T, d Demo, contents ...*genai.Content) *genai.Content {
	t.Helper()
	for resp, err := range d.GenerateContent(context.Background(), &model.LLMRequest{Contents: contents}, false) {
		if err != nil {
			t.Fatal(err)
		}
		return resp.Content
	}
	t.Fatal("no response")
	return nil
}

func callOf(c *genai.Content) *genai.FunctionCall {
	for _, p := range c.Parts {
		if p.FunctionCall != nil {
			return p.FunctionCall
		}
	}
	return nil
}

func textOf(c *genai.Content) string {
	var b strings.Builder
	for _, p := range c.Parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

const refund = "Open a refund for txn-2026-07-118845 at merchant A-114"

func TestDemoRefundFlow(t *testing.T) {
	t.Parallel()
	if (Demo{}).Name() != "demo-rules" {
		t.Error("name")
	}
	c := callOf(step(t, Demo{Sloppy: true}, user(refund)))
	if c == nil || c.Name != "open_refund_case" || c.Args["merchant_id"] != "undefined" {
		t.Fatalf("sloppy first call = %+v", c)
	}
	if c := callOf(step(t, Demo{}, user(refund))); c.Args["merchant_id"] != "A-114" {
		t.Fatalf("careful first call = %+v", c)
	}

	fix := step(t, Demo{}, user(refund), toolResponse("open_refund_case", map[string]any{"status": "invalid_argument", "field": "merchant_id"}))
	if !strings.Contains(textOf(fix), "re-extract merchant_id") || callOf(fix).Args["merchant_id"] != "A-114" {
		t.Errorf("fix = %+v", fix)
	}
	done := step(t, Demo{}, user(refund), toolResponse("open_refund_case", map[string]any{"status": "pending", "case_id": "rc-1", "replayed": true}))
	if !strings.Contains(textOf(done), "rc-1 is open") || !strings.Contains(textOf(done), "already existed") {
		t.Errorf("done = %q", textOf(done))
	}
	down := step(t, Demo{}, user(refund), toolResponse("open_refund_case", map[string]any{"status": "unavailable", "observation": "acquirer down"}))
	if !strings.Contains(textOf(down), "could not open") {
		t.Errorf("down = %q", textOf(down))
	}
}

func TestDemoOnlyReadsTheCurrentTurn(t *testing.T) {
	t.Parallel()
	// A finished refund in an earlier turn must not answer the new request.
	c := step(t, Demo{}, user(refund), toolResponse("open_refund_case", map[string]any{"status": "pending"}), user(refund))
	if callOf(c) == nil {
		t.Error("new turn must start a new call")
	}
}

func TestDemoRecallAndOtherIntents(t *testing.T) {
	t.Parallel()
	if c := callOf(step(t, Demo{}, user("What do you remember about A-114?"))); c.Name != "open_nodes" {
		t.Errorf("recall by id = %+v", c)
	}
	if c := callOf(step(t, Demo{}, user("What do you remember about delays?"))); c.Name != "search_nodes" {
		t.Errorf("recall by keyword = %+v", c)
	}
	graph := map[string]any{"entities": []any{map[string]any{"name": "A-114", "observations": []any{"t user: legacy"}}}}
	if got := textOf(step(t, Demo{}, user("what do you remember about A-114?"), toolResponse("open_nodes", graph))); !strings.Contains(got, "A-114: t user: legacy") {
		t.Errorf("summary = %q", got)
	}
	if got := textOf(step(t, Demo{}, user("recall delays"), toolResponse("search_nodes", map[string]any{"entities": []any{}}))); got != "I have no record of that." {
		t.Errorf("empty recall = %q", got)
	}
	if c := callOf(step(t, Demo{}, user("payouts for A-114"))); c.Name != "get_merchant_payouts" {
		t.Errorf("payouts = %+v", c)
	}
	if got := textOf(step(t, Demo{}, user("payouts for A-114"), toolResponse("get_merchant_payouts", map[string]any{"summary": "none"}))); !strings.Contains(got, "none") {
		t.Errorf("payouts answer = %q", got)
	}
	if got := textOf(step(t, Demo{}, user("Merchant A-114 is legacy"))); got != "Noted." {
		t.Errorf("fact = %q", got)
	}
	if got := textOf(step(t, Demo{}, user("write me a poem"))); !strings.Contains(got, "only handle") {
		t.Errorf("out of domain = %q", got)
	}
}

// usageRecorder is a Recorder that also takes token usage.
type usageRecorder struct {
	recorder
	prompt, output int32
	usageErr       error
}

func (u *usageRecorder) RecordLLMUsage(_ context.Context, _ string, p, o int32) error {
	u.prompt, u.output = p, o
	return u.usageErr
}

// metered answers in two chunks; the provider repeats UsageMetadata, the last
// one being the total.
type metered struct{}

func (metered) Name() string { return "metered" }
func (metered) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		_ = yield(&model.LLMResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 1}}, nil) &&
			yield(&model.LLMResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 120, CandidatesTokenCount: 17}}, nil)
	}
}

func TestCountingRecordsTokenUsage(t *testing.T) {
	t.Parallel()
	rec := &usageRecorder{}
	for _, err := range (Counting{Inner: metered{}, Recorder: rec}).GenerateContent(context.Background(), &model.LLMRequest{}, true) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if rec.prompt != 120 || rec.output != 17 || len(rec.calls) != 1 {
		t.Errorf("usage = %d/%d calls=%v, want the final 120/17 and one call", rec.prompt, rec.output, rec.calls)
	}

	// A recorder that cannot store usage fails the call loudly.
	bad := &usageRecorder{usageErr: errors.New("disk full")}
	var got error
	for _, err := range (Counting{Inner: metered{}, Recorder: bad}).GenerateContent(context.Background(), &model.LLMRequest{}, true) {
		if err != nil {
			got = err
		}
	}
	if got == nil || !strings.Contains(got.Error(), "disk full") {
		t.Errorf("err = %v", got)
	}

	// A plain Recorder (no UsageRecorder) is fine: only the call is counted.
	plain := &recorder{}
	for _, err := range (Counting{Inner: metered{}, Recorder: plain}).GenerateContent(context.Background(), &model.LLMRequest{}, true) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(plain.calls) != 1 {
		t.Errorf("plain calls = %v", plain.calls)
	}
}
