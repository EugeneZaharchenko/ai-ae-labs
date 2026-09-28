package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

func TestCoordinator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		wantCall string
		want     string
	}{
		{"billing", "I was charged twice this month", "billing_agent", "BIL-7"},
		{"tech", "The app crashes on login", "tech_agent", "cache"},
		{"unclear asks back", "hello", "", "billing or technical"},
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
			if tt.wantCall != "" && !tr.Called(tt.wantCall) {
				t.Errorf("%s not called; calls=%v", tt.wantCall, tr.Calls)
			}
			if tt.wantCall == "" && len(tr.Calls) != 0 {
				t.Errorf("unexpected delegation: %v", tr.Calls)
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
	if !strings.Contains(b.String(), "billing_agent") {
		t.Errorf("demo trace misses the delegation:\n%s", b.String())
	}
}
