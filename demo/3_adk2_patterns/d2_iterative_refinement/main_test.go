package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

func TestScore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		draft    string
		want     int
		wantGaps int
	}{
		{"Waterproof, breathable jacket, €129.", 100, 0},
		{"A jacket.", 25, 3},
		{"waterproof " + strings.Repeat("x ", 30), 25, 3},
	}
	for _, tt := range tests {
		got, gaps := score(tt.draft)
		if got != tt.want || len(gaps) != tt.wantGaps {
			t.Errorf("score(%q) = %d %v, want %d with %d gaps", tt.draft, got, gaps, tt.want, tt.wantGaps)
		}
	}
}

func TestRefine(t *testing.T) {
	t.Parallel()
	boom := errors.New("provider down")
	tests := []struct {
		name     string
		drafts   []string
		err      error
		wantBest int
		wantRuns int
		wantStop string
		wantErr  bool
	}{
		{
			name:     "stops early at threshold",
			drafts:   []string{"A jacket.", "Waterproof, breathable jacket, €129."},
			wantBest: 2, wantRuns: 2, wantStop: "threshold",
		},
		{
			name:     "keeps best, not last",
			drafts:   []string{"Waterproof breathable jacket.", "A jacket.", "A coat."},
			wantBest: 1, wantRuns: 3, wantStop: "cap",
		},
		{name: "generator error surfaces", err: boom, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var prompts []string
			gen := func(p string) (string, error) {
				prompts = append(prompts, p)
				if tt.err != nil {
					return "", tt.err
				}
				return tt.drafts[len(prompts)-1], nil
			}
			res, err := refine("brief", gen)
			if tt.wantErr {
				if !errors.Is(err, boom) {
					t.Fatalf("err = %v, want %v", err, boom)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.Best.N != tt.wantBest || len(res.Attempts) != tt.wantRuns || !strings.Contains(res.Stop, tt.wantStop) {
				t.Errorf("got best #%d, %d runs, stop %q", res.Best.N, len(res.Attempts), res.Stop)
			}
			if len(prompts) > 1 && !strings.Contains(prompts[1], "Fix:") {
				t.Errorf("second prompt carries no feedback: %q", prompts[1])
			}
		})
	}
}

func TestIterativeRefinement(t *testing.T) {
	t.Parallel()
	a, err := build(kit.Offline())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := kit.Run(context.Background(), a, io.Discard, spec.Input)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"attempt 3: score 50", "best #2 (75)"} {
		if !strings.Contains(tr.Final, want) {
			t.Errorf("final = %q, want %q", tr.Final, want)
		}
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "kept best, not last") {
		t.Errorf("demo trace misses the stop reason:\n%s", b.String())
	}
}
