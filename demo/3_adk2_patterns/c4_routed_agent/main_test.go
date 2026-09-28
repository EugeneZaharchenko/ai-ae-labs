package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

func TestRoutedAgent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"cheap tier", "What is my data limit?", []string{`"agent":"cheap_agent"`, `"failover":false`}},
		{"strong tier by rule", "Please compare plans A and B", []string{`"agent":"strong_agent"`, `"failover":false`}},
		{"failover to strong", "What is my data limit? (simulate outage)", []string{`"agent":"strong_agent"`, `"failover":true`}},
		{"failover to cheap", "Analyse my bill (simulate outage)", []string{`"agent":"cheap_agent"`, `"failover":true`}},
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
			for _, w := range tt.want {
				if !strings.Contains(tr.Final, w) {
					t.Errorf("final = %q, want %q", tr.Final, w)
				}
			}
		})
	}
}

func TestBothTiersDown(t *testing.T) {
	t.Parallel()
	down := func(string) kit.Brain {
		return func(kit.Prompt) kit.Reply { return kit.Reply{Err: errOutage} }
	}
	a, err := buildWith(kit.Offline(), down)
	if err != nil {
		t.Fatal(err)
	}
	_, err = kit.Run(context.Background(), a, io.Discard, "hi")
	if err == nil || !strings.Contains(err.Error(), "both tiers failed") {
		t.Fatalf("err = %v, want both tiers failed", err)
	}
	// RunNode keeps the sentinel, not the provider error value: the child's
	// error crosses the scheduler as text, so only ErrNodeFailed matches.
	if !errors.Is(err, workflow.ErrNodeFailed) {
		t.Errorf("err does not wrap workflow.ErrNodeFailed: %v", err)
	}
	if !strings.Contains(err.Error(), errOutage.Error()) {
		t.Errorf("err lost the provider message: %v", err)
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"failover":true`) {
		t.Errorf("demo trace misses the failover:\n%s", b.String())
	}
}
