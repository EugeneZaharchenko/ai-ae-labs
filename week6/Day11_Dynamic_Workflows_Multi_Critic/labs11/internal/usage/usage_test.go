package usage

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
)

func TestMeterAddAndTotals(t *testing.T) {
	t.Parallel()
	var m Meter
	m.Add("maker", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 100, CandidatesTokenCount: 40, ThoughtsTokenCount: 10})
	m.Add("maker", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 50, CandidatesTokenCount: 5})
	m.Add("critic", nil) // a provider that reports nothing is still a call

	if got := m.ByAgent()["maker"]; got != (Tokens{Prompt: 150, Output: 55, Calls: 2}) {
		t.Errorf("maker = %+v", got)
	}
	if got := m.ByAgent()["critic"]; got != (Tokens{Calls: 1}) {
		t.Errorf("critic = %+v, want one call and zero tokens", got)
	}
	if got := m.Total(); got.Calls != 3 || got.Total() != 205 || m.Spent() != 205 {
		t.Errorf("Total = %+v, Spent = %d", got, m.Spent())
	}
	tbl := m.Table()
	if !strings.Contains(tbl, "maker") || strings.Index(tbl, "critic") > strings.Index(tbl, "maker") {
		t.Errorf("Table not sorted by name:\n%s", tbl)
	}
}

func TestMeterIsRaceFree(t *testing.T) {
	t.Parallel()
	var m Meter
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.Add("a", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 1})
			}
		}()
	}
	wg.Wait()
	if got := m.Total(); got.Calls != 800 || got.Prompt != 800 {
		t.Errorf("Total = %+v", got)
	}
}

func TestCallbackCountsFinalResponsesOnly(t *testing.T) {
	t.Parallel()
	var m Meter
	cb := m.Callback("critic")
	u := &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 2}

	for _, r := range []struct {
		resp *model.LLMResponse
		err  error
	}{
		{&model.LLMResponse{UsageMetadata: u, Partial: true}, nil}, // streaming chunk
		{&model.LLMResponse{UsageMetadata: u}, errors.New("boom")}, // failed call
		{nil, nil},
		{&model.LLMResponse{UsageMetadata: u}, nil}, // the one that counts
	} {
		if out, err := cb(nil, r.resp, r.err); out != nil || err != nil {
			t.Fatalf("callback must never replace the response, got %v, %v", out, err)
		}
	}
	if got := m.ByAgent()["critic"]; got != (Tokens{Prompt: 10, Output: 2, Calls: 1}) {
		t.Errorf("critic = %+v", got)
	}
}

func TestPriceCost(t *testing.T) {
	t.Parallel()
	got := Price{InPerM: 2, OutPerM: 10}.Cost(Tokens{Prompt: 500_000, Output: 100_000})
	if got < 1.99 || got > 2.01 {
		t.Errorf("Cost = %v, want 2.00", got)
	}
}

func TestEstimatingFillsMissingUsageOnly(t *testing.T) {
	t.Parallel()
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText(strings.Repeat("x", 400), genai.RoleUser)}}
	est := Estimating(fakellm.New("m", fakellm.TextTurn(strings.Repeat("y", 80))))

	var got *model.LLMResponse
	for resp, err := range est.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
		got = resp
	}
	if got.UsageMetadata == nil || got.UsageMetadata.PromptTokenCount != 100 || got.UsageMetadata.CandidatesTokenCount != 20 {
		t.Fatalf("UsageMetadata = %+v, want 100 in / 20 out", got.UsageMetadata)
	}
	if est.Name() != "m" {
		t.Errorf("Name = %q", est.Name())
	}
}

func TestEstimatingPassesErrorsAndRealUsage(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	for _, err := range Estimating(fakellm.New("m", fakellm.Turn{Err: boom})).GenerateContent(context.Background(), &model.LLMRequest{}, false) {
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want boom", err)
		}
	}
}
