package main

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/adk/v2/session"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week2/internal/refund"
)

// The model classifier is the same graph with a different decision maker, so
// these tests assert the two things that follow from that: the route it picks
// is the one the model named, and an answer the graph cannot parse still lands
// on a branch instead of dead-ending the edge set.
func TestGraphWithModelClassifier(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  string
		answer string
		want   string
	}{
		{"refund", demoInput, "refund", "rc-txn-2026-07-118845-A-114"},
		{"out of domain", "привіт, як справи?", "out_of_domain", "поза цим доменом"},
		// A model that answers with a sentence, not one word. The route is
		// normalised out of it, so the graph still has an edge to take.
		{"sentence answer", "привіт, як справи?", "Відповідь: out_of_domain.", "поза цим доменом"},
		// An answer naming no route at all: the fallback is a real branch, so
		// the request is refused rather than dropped.
		{"unparseable answer", "привіт, як справи?", "IDK", "поза цим доменом"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &refund.Registry{}
			reg.SetClassifier(fakellm.New("classify", fakellm.TextTurn(tc.answer)))
			a, err := newGraph(reg)
			if err != nil {
				t.Fatal(err)
			}
			res, err := labrun.Run(t.Context(), a, tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(res.Final, tc.want) {
				t.Fatalf("final = %q, want %q", res.Final, tc.want)
			}
			// The decision is in the audit trail, whatever decided it.
			if !strings.Contains(eventRoutes(res.Events), `"routes"`) {
				t.Fatalf("no route in the event log: %+v", res.Events)
			}
		})
	}
}

// The model classifier drives the read path too: routing is not the only thing
// that changes, and a status request must still reach check_refund_status.
func TestGraphModelClassifierReachesStatusRoute(t *testing.T) {
	reg := &refund.Registry{}
	a, err := newGraph(reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := labrun.Run(t.Context(), a, demoInput); err != nil {
		t.Fatal(err)
	}
	// Only the second turn is model-classified: the register keeps the case the
	// first turn opened, which is what the read must find.
	reg.SetClassifier(fakellm.New("classify", fakellm.TextTurn("status")))
	a, err = newGraph(reg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := labrun.Run(t.Context(), a, "check status "+demoCaseID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Final, demoCaseID) || !strings.Contains(res.Final, "уже в реєстрі") {
		t.Fatalf("status via model routing = %q", res.Final)
	}
}

// A model failure is the model's failure: it must surface as an error, never
// be papered over by falling back to the rule classifier, which would hide a
// broken provider behind a plausible-looking route.
func TestGraphModelClassifierErrorSurfaces(t *testing.T) {
	reg := &refund.Registry{}
	reg.SetClassifier(fakellm.New("classify",
		fakellm.Turn{Err: errors.New("provider down")}))
	a, err := newGraph(reg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := labrun.Run(t.Context(), a, demoInput)
	if err == nil {
		t.Fatalf("expected the model failure to surface, got %q", res.Final)
	}
	if !strings.Contains(err.Error(), "classify with model") {
		t.Fatalf("error = %v, want it to name the classifier", err)
	}
}

// eventRoutes renders the routing events of a run, so a test can assert on what
// the graph actually recorded rather than on the final answer alone.
func eventRoutes(events []*session.Event) string {
	var b strings.Builder
	for _, e := range events {
		for _, r := range e.Routes {
			b.WriteString(`"routes":["` + r + `"],`)
		}
	}
	return b.String()
}
