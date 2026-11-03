package usage

import (
	"strings"
	"sync"
	"testing"

	"google.golang.org/genai"
)

func meta(prompt, candidates, thoughts int32) *genai.GenerateContentResponseUsageMetadata {
	return &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: prompt, CandidatesTokenCount: candidates,
		ThoughtsTokenCount: thoughts, TotalTokenCount: prompt + candidates + thoughts,
	}
}

func TestMeterCountsReasoningTokensAsOutput(t *testing.T) {
	// Reasoning tokens are billed at the output rate but reported in their own
	// field. Counting only Candidates understates the bill on exactly the models
	// this course is about.
	var m Meter
	m.Add(meta(1_000, 200, 50))
	got := m.Total()
	if got.Prompt != 1_000 || got.Output != 250 || got.Calls != 1 {
		t.Errorf("want 1000/250 over 1 call, got %+v", got)
	}
}

func TestMeterCountsACallThatReportsNoUsage(t *testing.T) {
	// A provider that says nothing must not vanish from the tally: "3 calls,
	// 0 tokens" is a fact worth seeing, and a missing row is not.
	var m Meter
	m.Add(nil)
	m.Add(meta(10, 5, 0))
	if got := m.Total(); got.Calls != 2 || got.Total() != 15 {
		t.Errorf("want 2 calls totalling 15, got %+v", got)
	}
}

func TestMeterIsSafeToFillConcurrently(t *testing.T) {
	// ADK runs a node's tool calls in parallel, so this is not hypothetical.
	var m Meter
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Add(meta(2, 1, 1))
		}()
	}
	wg.Wait()
	if got := m.Total(); got.Calls != 50 || got.Total() != 200 {
		t.Errorf("want 50 calls totalling 200, got %+v", got)
	}
}

func TestLineOmitsCostWithoutAPrice(t *testing.T) {
	// A $0.00 that means "no price configured" reads exactly like a $0.00 that
	// means "free", so the cost half is dropped rather than zeroed.
	tokens := Tokens{Prompt: 1_000_000, Output: 0, Calls: 1}

	bare := tokens.Line(nil)
	if !strings.Contains(bare, "1000000 in") || strings.Contains(bare, "$") {
		t.Errorf("unpriced line should carry tokens and no cost: %q", bare)
	}

	priced := tokens.Line(&Price{InPerM: 3, OutPerM: 15})
	if !strings.Contains(priced, "≈ $3.0000") {
		t.Errorf("priced line should cost 1M input at $3/1M: %q", priced)
	}
}

func TestCostSplitsInAndOut(t *testing.T) {
	p := Price{InPerM: 2, OutPerM: 10}
	got := p.Cost(Tokens{Prompt: 500_000, Output: 100_000})
	if want := 1.0 + 1.0; got != want {
		t.Errorf("want $%v, got $%v", want, got)
	}
}

func TestPerToolRanksBusiestFirst(t *testing.T) {
	got := PerTool(map[string]int{"a": 1, "b": 3, "c": 1})
	if !strings.HasPrefix(got, "tools: b×3, ") {
		t.Errorf("busiest tool should lead: %q", got)
	}
	// Ties break on name so the line is stable between runs.
	if got != "tools: b×3, a×1, c×1" {
		t.Errorf("want a stable, deterministic order, got %q", got)
	}
	if none := PerTool(nil); none != "tools: none called" {
		t.Errorf("want a named zero rather than an empty line, got %q", none)
	}
}
