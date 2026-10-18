// Package llm holds the worker-side model plumbing of the Day 8 lab:
//
//   - Counting wraps any model.LLM and records every real call in a durable
//     counter — the evidence that a crashed and resumed run did not call the
//     model again;
//   - Demo is a rule-based model, so the lab runs end to end with no API key.
//
// Both run inside the googleadk InvokeModel Activity, never in the workflow.
package llm

import (
	"context"
	"fmt"
	"iter"
	"regexp"
	"slices"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// Recorder persists one row per model call.
type Recorder interface {
	RecordLLMCall(ctx context.Context, model string) error
}

// UsageRecorder is optional: a Recorder that also implements it receives the
// token usage the provider reported (genai UsageMetadata) after each call.
// Tokens are what a restart actually costs; the call count only shows that it
// happened.
type UsageRecorder interface {
	RecordLLMUsage(ctx context.Context, model string, promptTokens, outputTokens int32) error
}

// Counting decorates a model with a durable call counter.
type Counting struct {
	Inner    model.LLM
	Recorder Recorder
}

// Name reports the real model's name.
func (c Counting) Name() string { return c.Inner.Name() }

// GenerateContent records the call, then delegates.
//
// req.Model arrives as the logical name the workflow used (googleadk ships
// only that name across the Activity boundary). Some backends — Gemini — call
// req.Model in preference to their own name, so it is reset to the real one.
func (c Counting) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if err := c.Recorder.RecordLLMCall(ctx, c.Inner.Name()); err != nil {
			yield(nil, fmt.Errorf("llm: record call: %w", err))
			return
		}
		r := *req
		r.Model = c.Inner.Name()
		var usage *genai.GenerateContentResponseUsageMetadata
		for resp, err := range c.Inner.GenerateContent(ctx, &r, stream) {
			if resp != nil && resp.UsageMetadata != nil {
				usage = resp.UsageMetadata // streaming repeats it; the last one is the total
			}
			if !yield(resp, err) {
				return
			}
		}
		if u, ok := c.Recorder.(UsageRecorder); ok && usage != nil {
			if err := u.RecordLLMUsage(ctx, c.Inner.Name(), usage.PromptTokenCount, usage.CandidatesTokenCount); err != nil {
				yield(nil, fmt.Errorf("llm: record usage: %w", err))
			}
		}
	}
}

// Demo is a deterministic stand-in for a model that knows the refund domain.
//
// Sloppy reproduces the 03:00 incident: its first open_refund_case call sends
// merchant_id "undefined", so the harness has something to correct.
type Demo struct {
	Sloppy bool
}

// Name implements model.LLM.
func (Demo) Name() string { return "demo-rules" }

var (
	txnRe      = regexp.MustCompile(`(?i)\btxn-\d{4}-\d{2}-\d+\b`)
	merchantRe = regexp.MustCompile(`\b[A-Z]-\d{2,}\b`)
	recallRe   = regexp.MustCompile(`(?i)\b(remember|recall|earlier|know about|told you|пам'ята|знаєш)\b`)
)

// GenerateContent decides the next step from the current turn only: the last
// user text and the tool responses that followed it.
func (d Demo) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: d.next(req.Contents)}, nil)
	}
}

type response struct {
	name string
	data map[string]any
}

// currentTurn returns the last user text and the tool responses after it.
func currentTurn(contents []*genai.Content) (string, []response) {
	user, start := "", 0
	for i, c := range contents {
		if c == nil || c.Role != genai.RoleUser {
			continue
		}
		for _, p := range c.Parts {
			if p != nil && strings.TrimSpace(p.Text) != "" {
				user, start = p.Text, i
			}
		}
	}
	var out []response
	for _, c := range contents[start:] {
		if c == nil {
			continue
		}
		for _, p := range c.Parts {
			if p != nil && p.FunctionResponse != nil {
				out = append(out, response{p.FunctionResponse.Name, p.FunctionResponse.Response})
			}
		}
	}
	return user, out
}

func (d Demo) next(contents []*genai.Content) *genai.Content {
	user, done := currentTurn(contents)
	n := len(done)
	call := func(name string, args map[string]any) *genai.Content {
		return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("call-%d", n+1), Name: name, Args: args},
		}}}
	}
	say := func(format string, args ...any) *genai.Content {
		return genai.NewContentFromText(fmt.Sprintf(format, args...), genai.RoleModel)
	}
	last := func(name string) (map[string]any, bool) {
		for i := len(done) - 1; i >= 0; i-- {
			if done[i].name == name {
				return done[i].data, true
			}
		}
		return nil, false
	}

	txn, merchant := txnRe.FindString(user), merchantRe.FindString(user)
	switch {
	case recallRe.MatchString(user):
		ids := slices.DeleteFunc([]string{merchant, txn}, func(s string) bool { return s == "" })
		g, ok := last("open_nodes")
		if !ok && len(ids) > 0 {
			return call("open_nodes", map[string]any{"names": ids})
		}
		if !ok {
			if g, ok = last("search_nodes"); !ok {
				return call("search_nodes", map[string]any{"query": user})
			}
		}
		return say("%s", summarise(g))

	case txn != "" && merchant != "":
		res, ok := last("open_refund_case")
		if !ok {
			m := merchant
			if d.Sloppy {
				m = "undefined"
			}
			return call("open_refund_case", map[string]any{"transaction_id": txn, "merchant_id": m})
		}
		switch res["status"] {
		case "invalid_argument":
			return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{
				{Text: fmt.Sprintf("I should re-extract %v from the request.", res["field"])},
				{FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("call-%d", n+1), Name: "open_refund_case",
					Args: map[string]any{"transaction_id": txn, "merchant_id": merchant}}},
			}}
		case "pending":
			replayed := ""
			if res["replayed"] == true {
				replayed = " (it already existed — nothing new was created)"
			}
			return say("Refund case %v is open for %s at merchant %s%s.", res["case_id"], txn, merchant, replayed)
		default:
			return say("I could not open the refund case: %v", res["observation"])
		}

	case merchant != "" && strings.Contains(strings.ToLower(user), "payout"):
		res, ok := last("get_merchant_payouts")
		if !ok {
			return call("get_merchant_payouts", map[string]any{"merchant_id": merchant})
		}
		return say("Payouts for %s: %v", merchant, res["summary"])

	case merchant != "" || txn != "":
		return say("Noted.")
	default:
		return say("I only handle LEDGERWORKS refund cases, merchant payouts and what you told me about them earlier.")
	}
}

// summarise turns a knowledge graph response into one answer.
func summarise(g map[string]any) string {
	ents, _ := g["entities"].([]any)
	if len(ents) == 0 {
		return "I have no record of that."
	}
	var b strings.Builder
	b.WriteString("From earlier conversations:")
	for _, e := range ents {
		m, _ := e.(map[string]any)
		obs, _ := m["observations"].([]any)
		for _, o := range obs {
			fmt.Fprintf(&b, "\n- %v: %v", m["name"], o)
		}
	}
	return b.String()
}
