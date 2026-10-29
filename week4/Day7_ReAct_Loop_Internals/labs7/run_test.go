package main

import (
	"context"
	"slices"
	"testing"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
)

// `go run . graph console` must hand MY graph agent to the launcher, and
// without a key the brain is the RulePlanner, so the run is offline.
func TestRunSubcommandsReachTheLauncher(t *testing.T) {
	offlineEnv(t)
	orig := runLauncher
	t.Cleanup(func() { runLauncher = orig })

	var got agent.Agent
	var gotArgs []string
	runLauncher = func(_ context.Context, a agent.Agent, args []string) error {
		got, gotArgs = a, args
		return nil
	}

	tests := []struct {
		args      []string
		wantAgent string
		wantArgs  []string
	}{
		{[]string{"graph", "console"}, "react_graph_agent", []string{"console"}},
		{[]string{"team", "web", "webui", "api"}, "ledgerworks_team", []string{"web", "webui", "api"}},
		{[]string{"console"}, "", []string{"console"}},
	}
	for _, tt := range tests {
		got, gotArgs = nil, nil
		if err := run(context.Background(), tt.args); err != nil {
			t.Fatalf("run(%q): %v", tt.args, err)
		}
		if got == nil || (tt.wantAgent != "" && got.Name() != tt.wantAgent) || !slices.Equal(gotArgs, tt.wantArgs) {
			t.Errorf("run(%q) gave the launcher %v with %q", tt.args, got, gotArgs)
		}
	}

	// The graph from `graph console` really runs the loop to a ticket.
	if err := run(context.Background(), []string{"graph", "console"}); err != nil {
		t.Fatal(err)
	}
	res, err := labrun.Run(context.Background(), got, defaultTask)
	if err != nil {
		t.Fatalf("graph run: %v", err)
	}
	if res.Final == "" {
		t.Error("graph returned no answer")
	}
}

// One-shot modes print the trace and do not need a launcher.
func TestRunOneShotModes(t *testing.T) {
	offlineEnv(t)
	for _, args := range [][]string{{"agent"}, {"team", "Check the payout incident for A-114"}} {
		if err := run(context.Background(), args); err != nil {
			t.Errorf("run(%q): %v", args, err)
		}
	}
}
