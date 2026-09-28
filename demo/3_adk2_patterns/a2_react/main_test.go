package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

func TestReact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		brain     kit.Brain
		input     string
		want      string
		wantSteps string
	}{
		{"re-plans after empty result", plannerBrain, "When was ADK Go 2.0 announced?", "announced in 2026", `"steps":3`},
		{"first query hits", plannerBrain, "How does the graph engine work?", "graph nodes", `"steps":2`},
		{"never finishes hits the cap", func(kit.Prompt) kit.Reply { return kit.Say("SEARCH: nothing") }, "q", "step budget", `"capped":true`},
		{"protocol break is recorded, loop still capped", func(kit.Prompt) kit.Reply { return kit.Say("hmm") }, "q", "step budget", `"steps":4`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := newReact(kit.NewModel("planner", tt.brain))
			if err != nil {
				t.Fatal(err)
			}
			var b strings.Builder
			tr, err := kit.Run(context.Background(), a, &b, tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(tr.Final, tt.want) || !strings.Contains(tr.Final, tt.wantSteps) {
				t.Errorf("final = %q, want %q and %q\n%s", tr.Final, tt.want, tt.wantSteps, b.String())
			}
		})
	}
}

func TestHelpers(t *testing.T) {
	t.Parallel()
	pad := scratchpad("Q?", []string{"Action: search a", "Observation: x"})
	if question(pad) != "Q?" || lastObservation(pad) != "x" {
		t.Errorf("pad parse failed: %q", pad)
	}
	if lastObservation("Question: Q") != "" {
		t.Error("no observation expected")
	}
}

func TestBuild(t *testing.T) {
	t.Parallel()
	a, err := build(kit.Offline())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kit.Run(context.Background(), a, io.Discard, spec.Input); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "(no results)") {
		t.Errorf("demo trace misses the empty observation:\n%s", b.String())
	}
}
