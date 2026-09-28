package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

// stubborn makes the cost role object forever; everyone else behaves.
func stubborn(r role) kit.Brain {
	if r.name == "cost" {
		return func(kit.Prompt) kit.Reply { return kit.Say("cost: PROPOSE cheaper, always") }
	}
	return roleBrain(r)
}

func TestSwarm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		brains    func(role) kit.Brain
		want      string
		wantTurns int
	}{
		{"converges in round 2", roleBrain, "consensus in round 2", 2 * len(roles)},
		{"round cap stops a stubborn role", stubborn, "no consensus after 4 rounds", maxRounds * len(roles)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := buildWith(kit.Offline(), tt.brains)
			if err != nil {
				t.Fatal(err)
			}
			tr, err := kit.Run(context.Background(), a, io.Discard, "Design a cable")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(tr.Final, tt.want) {
				t.Errorf("final = %q, want %q", tr.Final, tt.want)
			}
			turns := 0
			for _, ev := range tr.Events {
				if ev.Content != nil && ev.Author != "swarm" {
					turns++
				}
			}
			if turns != tt.wantTurns {
				t.Errorf("role turns = %d, want %d", turns, tt.wantTurns)
			}
		})
	}
}

func TestAfterAgree(t *testing.T) {
	t.Parallel()
	if got := afterAgree("x: AGREE final"); got != "final" {
		t.Errorf("afterAgree = %q", got)
	}
	if got := afterAgree("no token"); got != "no token" {
		t.Errorf("afterAgree = %q", got)
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "consensus") {
		t.Errorf("demo trace misses the outcome:\n%s", b.String())
	}
}
