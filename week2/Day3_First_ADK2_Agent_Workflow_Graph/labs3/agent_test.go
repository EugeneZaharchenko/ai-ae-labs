package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week2/internal/refund"
)

func TestEventLogIsAuditable(t *testing.T) {
	for _, name := range []string{"LlmAgent", "workflow-граф"} {
		t.Run(name, func(t *testing.T) {
			reg := &refund.Registry{}
			var a agent.Agent
			var err error
			if name == "LlmAgent" {
				a, err = newLiveAgent(fakellm.New("scripted",
					fakellm.CallTurn("open_refund_case", map[string]any{
						"transaction_id": "txn-2026-07-118845", "merchant_id": "A-114",
					}),
					fakellm.TextTurn("Кейс відкрито.")), reg)
			} else {
				a, err = refund.NewGraph(reg)
			}

			if err != nil {
				t.Fatal(err)
			}
			res, err := labrun.Run(t.Context(), a, demoInput)
			if err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, event := range res.Events {
				delta := event.Actions.StateDelta
				if delta[refund.StateKeyCaseID] == "rc-txn-2026-07-118845-A-114" {
					found++
					if delta[refund.StateKeyStatus] != "pending" || delta[refund.StateKeyMerchantID] != "A-114" {
						t.Fatalf("unexpected delta: %v", delta)
					}
				}
			}
			if found != 1 {
				t.Fatalf("refund events = %d, want 1", found)
			}
			if len(res.Events) < 3 || res.Final == "" {
				t.Fatalf("incomplete run: %+v", res)
			}
		})
	}
}

func TestDemo(t *testing.T) {
	var first, second bytes.Buffer
	for _, out := range []*bytes.Buffer{&first, &second} {
		if err := runDemo(t.Context(), out, demoInput); err != nil {
			t.Fatal(err)
		}
	}
	if first.String() != second.String() {
		t.Fatal("normalized output changes across identical runs")
	}
	for _, want := range []string{"refund:last_case_id", "pending", "rc-txn-2026-07-118845-A-114"} {
		if !strings.Contains(first.String(), want) {
			t.Fatalf("missing %q in %s", want, first.String())
		}
	}
}

func TestDemoErrors(t *testing.T) {
	for _, input := range []string{"", "txn-123 Z-999"} {
		if err := runDemo(t.Context(), &bytes.Buffer{}, input); err == nil {
			t.Fatalf("expected error for %q", input)
		}
	}
	if err := runDemo(context.Background(), brokenWriter{}, demoInput); !errors.Is(err, errWrite) {
		t.Fatalf("write error = %v", err)
	}
}

var errWrite = errors.New("writer closed")

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errWrite }

func TestLiveRunUsesInjectedModel(t *testing.T) {
	m := fakellm.New("scripted",
		fakellm.CallTurn("open_refund_case", map[string]any{
			"transaction_id": "txn-2026-07-118845", "merchant_id": "A-114",
		}), fakellm.TextTurn("Кейс відкрито."))
	a, err := newLiveAgent(m, &refund.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runAgent(t.Context(), a, &out, demoInput); err != nil {
		t.Fatal(err)
	}
	if m.Remaining() != 0 || !strings.Contains(out.String(), "functionCall") || !strings.Contains(out.String(), "refund:last_case_id") {
		t.Fatalf("live agent path did not execute the supplied model/tool: %s", out.String())
	}
}

func TestFluentAnswerWithoutSideEffect(t *testing.T) {
	reg := &refund.Registry{}
	a, err := newLiveAgent(fakellm.New("scripted", fakellm.TextTurn("Кейс відкрито.")), reg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := labrun.Run(t.Context(), a, demoInput)
	if err != nil {
		t.Fatal(err)
	}
	if res.Final == "" || res.CalledTool("open_refund_case") {
		t.Fatalf("expected fluent answer without tool call: %+v", res)
	}
	for _, event := range res.Events {
		if len(event.Actions.StateDelta) != 0 {
			t.Fatalf("unexpected delta: %v", event.Actions.StateDelta)
		}
	}

	// A first real call against the same registry must still create this case.
	graph, err := refund.NewGraph(reg)
	if err != nil {
		t.Fatal(err)
	}
	after, err := labrun.Run(t.Context(), graph, demoInput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(after.Final, "pending") {
		t.Fatalf("model text unexpectedly created a case: %s", after.Final)
	}
}

func TestModelErrorDoesNotFallBack(t *testing.T) {
	a, err := newLiveAgent(fakellm.New("exhausted"), &refund.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runAgent(t.Context(), a, &out, demoInput); err == nil {
		t.Fatal("model failure must surface, not become a fake or graph success")
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected success output: %s", out.String())
	}
}
