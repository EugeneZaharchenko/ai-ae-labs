// Package usage turns the token counts a provider reports into numbers an
// example can print and budget against.
//
// The counts come from genai UsageMetadata on each model.LLMResponse — the
// provider's own tally, not a len(text)/4 guess. Every week-4-onward example
// calls a real model, and a real model costs real money, so "how much did this
// run cost" is part of the demo rather than a footnote.
//
// The package deliberately carries NO price table. Prices change monthly, and a
// number baked into a course example is wrong by the time the next cohort reads
// it. The caller supplies today's rate — in these examples, a -price-in and
// -price-out flag — and with neither set the example prints tokens alone.
//
// Same shape as the labs' internal/usage, so a learner who reads the example and
// then the lab sees one idea, not two.
package usage

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"google.golang.org/genai"
)

// Tokens is a token tally. Calls counts model turns, including any that
// reported no usage, so a provider that says nothing shows up as
// "N calls, 0 tokens" instead of vanishing.
type Tokens struct {
	Prompt int
	Output int
	Calls  int
}

// Total is prompt plus output tokens.
func (t Tokens) Total() int { return t.Prompt + t.Output }

// Meter accumulates usage. The zero value is ready, and it is safe for
// concurrent use: ADK runs a node's tool calls in parallel by default, so a
// tally filled from inside them is filled from several goroutines.
type Meter struct {
	mu   sync.Mutex
	seen Tokens
}

// Add records one model response.
func (m *Meter) Add(u *genai.GenerateContentResponseUsageMetadata) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen.Calls++
	if u == nil {
		return
	}
	m.seen.Prompt += int(u.PromptTokenCount)
	// Reasoning tokens are billed as output but reported in their own field.
	m.seen.Output += int(u.CandidatesTokenCount) + int(u.ThoughtsTokenCount)
}

// Total returns the tally so far.
func (m *Meter) Total() Tokens {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen
}

// Spent is the single number a budget compares against.
func (m *Meter) Spent() int { return m.Total().Total() }

// Price is a provider rate in US dollars per million tokens.
type Price struct {
	InPerM  float64
	OutPerM float64
}

// Cost converts a tally into dollars.
func (p Price) Cost(t Tokens) float64 {
	return float64(t.Prompt)/1e6*p.InPerM + float64(t.Output)/1e6*p.OutPerM
}

// Line renders the tally for a console. price may be nil, in which case the
// cost half is omitted rather than shown as $0.00 — a zero that means "not
// configured" reads exactly like a zero that means "free".
func (t Tokens) Line(price *Price) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tokens: %d in + %d out = %d over %d model calls",
		t.Prompt, t.Output, t.Total(), t.Calls)
	if price != nil {
		fmt.Fprintf(&b, "  ≈ $%.4f at $%.2f/$%.2f per 1M", price.Cost(t), price.InPerM, price.OutPerM)
	}
	return b.String()
}

// PerTool renders how often each tool was called, busiest first. It is the
// cheapest honest signal of whether a loop is doing work or spinning.
func PerTool(counts map[string]int) string {
	if len(counts) == 0 {
		return "tools: none called"
	}
	names := make([]string, 0, len(counts))
	for n := range counts {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s×%d", n, counts[n]))
	}
	return "tools: " + strings.Join(parts, ", ")
}
