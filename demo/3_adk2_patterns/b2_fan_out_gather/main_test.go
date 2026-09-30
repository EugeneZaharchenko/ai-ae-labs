package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

func TestFanOut(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		limit    int
		wantPeak int
	}{
		{"cap of 2 holds", 2, 2},
		{"cap of 1 serialises", 1, 1},
		{"no cap runs all three", 0, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, g, err := newGraph(kit.Offline(), tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			tr, err := kit.Run(context.Background(), a, io.Discard, spec.Input)
			if err != nil {
				t.Fatal(err)
			}
			if got := g.Peak(); got != tt.wantPeak {
				t.Errorf("peak concurrency = %d, want %d", got, tt.wantPeak)
			}
			if !strings.HasPrefix(tr.Final, "VERDICT: buy") {
				t.Errorf("final = %q", tr.Final)
			}
		})
	}
}

// TestFanInNeedsJoinNode shows the mechanical guard: merging branches
// straight into an ordinary node does not build.
func TestFanInNeedsJoinNode(t *testing.T) {
	t.Parallel()
	g := &gauge{}
	a, b := research("a", "x", g), research("b", "y", g)
	sink := workflow.NewFunctionNode("sink", func(_ agent.Context, in any) (any, error) { return in, nil }, workflow.NodeConfig{})
	edges := workflow.NewEdgeBuilder().
		AddFanOut(workflow.Start, a, b).
		AddFanIn(sink, a, b).
		Build()
	if _, err := workflow.New("bad", edges); !errors.Is(err, workflow.ErrUnsupportedFanIn) {
		t.Fatalf("err = %v, want ErrUnsupportedFanIn", err)
	}
}

func TestVerdictBrainNeedsData(t *testing.T) {
	t.Parallel()
	if r := verdictBrain(kit.Prompt{Text: "- pricing → ?"}); !strings.Contains(r.Text, "need more data") {
		t.Errorf("reply = %q", r.Text)
	}
}

func TestBuild(t *testing.T) {
	t.Parallel()
	if _, err := build(kit.Offline()); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "gather ⇒") {
		t.Errorf("demo trace misses the join output:\n%s", b.String())
	}
}
