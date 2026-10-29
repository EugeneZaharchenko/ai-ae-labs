// Package usage turns the token counts a provider reports into numbers the lab
// can budget and bill against.
//
// Tokens come from genai UsageMetadata on each model.LLMResponse — the
// provider's own count, not a len(text)/4 guess. They are collected in an
// llmagent AfterModelCallback, which is the ADK hook the framework documents for
// "collect metrics on token usage". A Meter is shared by every agent of a run,
// so the loop can ask one question — "how much have we spent?" — before it
// authorises another attempt.
//
// Estimating exists only for offline tests: scripted models return no usage, and
// a budget that is never exercised is a budget that does not work.
package usage

import (
	"context"
	"fmt"
	"iter"
	"sort"
	"strings"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// Tokens is a token tally.
type Tokens struct {
	Prompt int
	Output int
	Calls  int
}

// Total is prompt plus output tokens.
func (t Tokens) Total() int { return t.Prompt + t.Output }

// Meter accumulates token usage per agent. The zero value is ready; it is safe
// for concurrent use (parallel critics report from different goroutines).
type Meter struct {
	mu      sync.Mutex
	byAgent map[string]Tokens
}

// Add records one model response for the named agent. A response without usage
// metadata still counts as a call, so a provider that reports nothing shows up
// as "N calls, 0 tokens" instead of vanishing.
func (m *Meter) Add(agentName string, u *genai.GenerateContentResponseUsageMetadata) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.byAgent == nil {
		m.byAgent = map[string]Tokens{}
	}
	t := m.byAgent[agentName]
	t.Calls++
	if u != nil {
		t.Prompt += int(u.PromptTokenCount)
		// Reasoning tokens are billed as output but reported separately.
		t.Output += int(u.CandidatesTokenCount) + int(u.ThoughtsTokenCount)
	}
	m.byAgent[agentName] = t
}

// Callback returns the AfterModelCallback that feeds this meter. It never
// replaces the response: returning (nil, nil) keeps the model's own.
func (m *Meter) Callback(agentName string) llmagent.AfterModelCallback {
	return func(_ agent.Context, resp *model.LLMResponse, err error) (*model.LLMResponse, error) {
		if err == nil && resp != nil && !resp.Partial {
			m.Add(agentName, resp.UsageMetadata)
		}
		return nil, nil
	}
}

// Total sums every agent.
func (m *Meter) Total() Tokens {
	m.mu.Lock()
	defer m.mu.Unlock()
	var sum Tokens
	for _, t := range m.byAgent {
		sum.Prompt += t.Prompt
		sum.Output += t.Output
		sum.Calls += t.Calls
	}
	return sum
}

// Spent is the single number the loop's budget compares against.
func (m *Meter) Spent() int { return m.Total().Total() }

// ByAgent returns a copy of the per-agent tally.
func (m *Meter) ByAgent() map[string]Tokens {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]Tokens, len(m.byAgent))
	for k, v := range m.byAgent {
		out[k] = v
	}
	return out
}

// Table renders the per-agent tally, sorted by name, for logs and READMEs.
func (m *Meter) Table() string {
	by := m.ByAgent()
	names := make([]string, 0, len(by))
	for n := range by {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "%-24s %6s %8s %8s\n", "agent", "calls", "prompt", "output")
	for _, n := range names {
		t := by[n]
		fmt.Fprintf(&b, "%-24s %6d %8d %8d\n", n, t.Calls, t.Prompt, t.Output)
	}
	return b.String()
}

// Price is a provider price in USD per million tokens. Prices change monthly:
// the lab never hardcodes one — the caller supplies today's numbers.
type Price struct {
	InPerM  float64
	OutPerM float64
}

// Cost converts a tally into dollars.
func (p Price) Cost(t Tokens) float64 {
	return float64(t.Prompt)/1e6*p.InPerM + float64(t.Output)/1e6*p.OutPerM
}

// Estimating wraps a model and fills UsageMetadata when the inner model left it
// empty, at four characters per token. Offline tests only: it proves the budget
// wiring, not any real cost.
func Estimating(inner model.LLM) model.LLM { return estimating{inner} }

type estimating struct{ model.LLM }

func (e estimating) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		in := 0
		for _, c := range req.Contents {
			in += contentLen(c)
		}
		if req.Config != nil {
			in += contentLen(req.Config.SystemInstruction)
		}
		for resp, err := range e.LLM.GenerateContent(ctx, req, stream) {
			if resp != nil && resp.UsageMetadata == nil {
				resp.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{
					PromptTokenCount:     int32(in / 4),
					CandidatesTokenCount: int32(contentLen(resp.Content) / 4),
				}
			}
			if !yield(resp, err) {
				return
			}
		}
	}
}

func contentLen(c *genai.Content) int {
	if c == nil {
		return 0
	}
	n := 0
	for _, p := range c.Parts {
		if p != nil {
			n += len(p.Text)
		}
	}
	return n
}
