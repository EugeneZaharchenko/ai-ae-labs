package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

func TestHierarchy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		// Both leaves of research_manager and the editor under writing_manager
		// must contribute: that proves both levels ran and synthesised upward.
		{"ev market", "Competitive analysis of EV chargers", []string{"Report:", "2.1M", "Ionity"}},
		{"any goal", "Analyse heat pumps", []string{"Report:", "market:", "competitors:"}},
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
			// The lead only sees its direct children; the analysts run in
			// isolated sessions and never appear in the lead's trace.
			for _, c := range []string{"research_manager", "writing_manager"} {
				if !tr.Called(c) {
					t.Errorf("%s not called; calls=%v", c, tr.Calls)
				}
			}
			if tr.Called("market_analyst") {
				t.Errorf("grandchild leaked into the root trace: %v", tr.Calls)
			}
			for _, w := range tt.want {
				if !strings.Contains(tr.Final, w) {
					t.Errorf("final = %q, want %q", tr.Final, w)
				}
			}
		})
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "writing_manager") {
		t.Errorf("demo trace misses the delegation:\n%s", b.String())
	}
}
