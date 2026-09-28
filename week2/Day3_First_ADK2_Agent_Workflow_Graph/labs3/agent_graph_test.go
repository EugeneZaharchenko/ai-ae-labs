package main

import (
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week2/internal/refund"
)

const demoCaseID = "rc-txn-2026-07-118845-A-114"

// The graph owns its topology here, so its tests live here too: what
// agent_graph.go wires is what this file drives.
//
// One case per route, because the route is the decision the graph makes before
// any step runs. A rejection is a route that reached a step and failed there,
// not a request the graph could not classify: an unclassifiable request now
// takes the out_of_domain branch and gets an answer.
func TestGraph(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		bad   bool
		want  string
	}{
		{"refund", "txn-123 A-114", false, "rc-txn-123-A-114"},
		{"refund rejected", "txn-123 Z-999", true, ""},
		{"status of an unopened case", "check status rc-txn-123-A-114", true, ""},
		{"status without a case ID", "статус", true, ""},
		{"out of domain", "привіт, як справи?", false, "поза цим доменом"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &refund.Registry{}
			a, err := newGraph(reg)
			if err != nil {
				t.Fatal(err)
			}
			res, err := labrun.Run(t.Context(), a, tc.input)
			if (err != nil) != tc.bad {
				t.Fatalf("run = %+v, %v", res, err)
			}
			if !tc.bad && !strings.Contains(res.Final, tc.want) {
				t.Fatalf("final = %q, want it to contain %q", res.Final, tc.want)
			}
		})
	}
}

// The status route exists because a model that can only open cases answers
// status questions from its own head. This drives the read path through the
// graph instead: open a case, then ask about it, in one process.
func TestGraphStatusRouteReadsTheRegister(t *testing.T) {
	reg := &refund.Registry{}
	a, err := newGraph(reg)
	if err != nil {
		t.Fatal(err)
	}
	// A Runner, not labrun.Run: the second turn must see the registry the
	// first turn wrote to, which is the whole point of the read path.
	r := labrun.NewRunner(a)
	opened, err := r.Turn(t.Context(), "lab-user", "lab-session", demoInput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(opened.Final, demoCaseID) || !strings.Contains(opened.Final, "pending") {
		t.Fatalf("open = %q", opened.Final)
	}
	// The bare case ID carries no verb, and is still a status request: asking
	// about a case cannot mean anything else.
	for _, input := range []string{
		"check status " + demoCaseID,
		demoCaseID,
		"перевір статус кейса " + demoCaseID,
	} {
		t.Run(input, func(t *testing.T) {
			got, err := r.Turn(t.Context(), "lab-user", "lab-session", input)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got.Final, demoCaseID) || !strings.Contains(got.Final, "уже в реєстрі") {
				t.Fatalf("status = %q", got.Final)
			}
			// Reading is a business event: the answer names what it read.
			if !deltaCarries(got, refund.StateKeyCaseID, demoCaseID) {
				t.Fatalf("status turn left no case ID in the log: %+v", got.Events)
			}
		})
	}
	// Reading must not change the case. "already_open" belongs to a second
	// open, never to a read.
	again, err := r.Turn(t.Context(), "lab-user", "lab-session", demoInput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(again.Final, "already_open") {
		t.Fatalf("reopen = %q", again.Final)
	}
}

// An ID that was never opened is an error, not a status. This is the line
// between reading the register and inventing an answer.
func TestGraphUnknownCaseIsNeverInvented(t *testing.T) {
	for _, input := range []string{
		"check status rc-txn-999-A-114",
		"rc-txn-999-A-114",
	} {
		t.Run(input, func(t *testing.T) {
			a, err := newGraph(&refund.Registry{})
			if err != nil {
				t.Fatal(err)
			}
			res, err := labrun.Run(t.Context(), a, input)
			if err == nil {
				t.Fatalf("an unopened case produced a status: %q", res.Final)
			}
			if strings.Contains(res.Final, "pending") || strings.Contains(res.Final, "already_open") {
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

// deltaCarries reports whether any event put key=want into its state delta.
func deltaCarries(res labrun.Result, key, want string) bool {
	for _, event := range res.Events {
		if event.Actions.StateDelta[key] == want {
			return true
		}
	}
	return false
}
