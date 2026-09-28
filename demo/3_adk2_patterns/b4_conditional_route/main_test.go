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

func TestRoutes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bug", "The app crashes with an error", "bug_desk"},
		{"support", "how do I reset my password", "support_desk"},
		{"default catches the rest", "invoice please", "human_triage"},
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
			if !strings.HasPrefix(tr.Final, tt.want) {
				t.Errorf("final = %q, want branch %s", tr.Final, tt.want)
			}
		})
	}
}

func TestLabel(t *testing.T) {
	t.Parallel()
	tests := map[string]string{"BUG": "BUG", " support\n": "SUPPORT", "It is a bug.": "BUG", "OTHER": ""}
	for in, want := range tests {
		if got := label(in); got != want {
			t.Errorf("label(%q) = %q, want %q", in, got, want)
		}
	}
}

// Two Default edges on one node are rejected when the graph is built.
func TestTwoDefaultsRejected(t *testing.T) {
	t.Parallel()
	router := workflow.NewFunctionNode("dispatch", func(agent.Context, any) (string, error) { return "", nil }, workflow.NodeConfig{})
	edges := workflow.NewEdgeBuilder().
		Add(workflow.Start, router).
		AddRoute(router, desk("a", "x"), workflow.Default).
		AddRoute(router, desk("b", "y"), workflow.Default).
		Build()
	if _, err := workflow.New("bad", edges); !errors.Is(err, workflow.ErrMultipleDefaultRoutes) {
		t.Fatalf("err = %v, want ErrMultipleDefaultRoutes", err)
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "route=[BUG]") {
		t.Errorf("demo trace misses the BUG route:\n%s", b.String())
	}
}
