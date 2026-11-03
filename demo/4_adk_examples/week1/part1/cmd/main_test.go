package main

import (
	"context"
	"testing"

	"google.golang.org/adk/v2/agent"
)

func TestBenchmarkModels(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	result, err := benchmarkModels(ctx, "compare top models for coding")
	if err != nil {
		t.Fatalf("benchmarkModels failed: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty benchmark result")
	}
	if len(result) < 100 {
		t.Fatalf("expected detailed table, got %d chars", len(result))
	}
}

func TestBenchmarkContainsModels(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	result, _ := benchmarkModels(ctx, "test")
	for _, model := range []string{"Claude", "Gemini", "GPT", "Grok", "DeepSeek"} {
		if !contains(result, model) {
			t.Errorf("expected result to contain %q", model)
		}
	}
}

func TestBenchmarkContainsAxes(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	result, _ := benchmarkModels(ctx, "test")
	if !contains(result, "task type") || !contains(result, "latency") {
		t.Error("expected decision framework axes in output")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
