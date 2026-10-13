package main

import (
	"testing"

	"github.com/dimetron/ai-eng-course/labs/week2/internal/refund"
)

// HW4: appName in /api/run_sse must be the agent name, so check it here
// and not only through the HTTP test.
func TestGraphName(t *testing.T) {
	a, err := newGraph(&refund.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Name() != "first_graph_agent" {
		t.Errorf("agent name = %q, want first_graph_agent (used as appName in REST)", a.Name())
	}
}
