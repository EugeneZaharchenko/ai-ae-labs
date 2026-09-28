package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

func TestSingleAgent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		wantCalls []string
		want      string
	}{
		{"known order", "Where is my order ORD-42?", []string{"get_order", "check_delivery"}, "ETA 2026-10-01"},
		{"no id asks back", "where is my parcel", nil, "Which order?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := build(kit.Offline())
			if err != nil {
				t.Fatal(err)
			}
			tr, err := kit.Run(context.Background(), a, io.Discard, tt.input)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range tt.wantCalls {
				if !tr.Called(c) {
					t.Errorf("tool %s not called; calls=%v", c, tr.Calls)
				}
			}
			if !strings.Contains(tr.Final, tt.want) {
				t.Errorf("final = %q, want %q", tr.Final, tt.want)
			}
		})
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "get_order") {
		t.Errorf("demo trace misses the tool call:\n%s", b.String())
	}
}
