package main

import (
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week2/internal/refund"
)

// The graph owns its topology here, so its tests live here too: what
// agent_graph.go wires is what this file drives.
func TestGraph(t *testing.T) {
	for _, tc := range []struct {
		input string
		bad   bool
	}{
		{"txn-123 A-114", false},
		{"hello", true},
		{"txn-123 Z-999", true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			reg := &refund.Registry{}
			a, err := newGraph(reg)
			if err != nil {
				t.Fatal(err)
			}
			res, err := labrun.Run(t.Context(), a, tc.input)
			if (err != nil) != tc.bad {
				t.Fatalf("run = %+v, %v", res, err)
			}
			if !tc.bad && !strings.Contains(res.Final, "rc-txn-123-A-114") {
				t.Fatalf("final = %q", res.Final)
			}
		})
	}
}

// A rejected request must leave the register untouched. The registry is
// unexported, so this asks it a question instead of looking inside: a request
// that was never recorded opens as "pending", never as "already_open".
func TestGraphRejectedRequestCreatesNoCase(t *testing.T) {
	reg := &refund.Registry{}
	a, err := newGraph(reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := labrun.Run(t.Context(), a, "txn-123 Z-999"); err == nil {
		t.Fatal("expected the unknown merchant to be rejected")
	}
	res, err := labrun.Run(t.Context(), a, "txn-123 A-114")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Final, "pending") {
		t.Fatalf("rejected request left state behind: %q", res.Final)
	}
}
