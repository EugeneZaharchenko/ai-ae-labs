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
				a, err = newGraph(reg)
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
	// The routing decision is part of the audit trail, not a detail of the
	// engine: without it the log shows a request and a result but not why the
	// graph took this branch.
	for _, want := range []string{"refund:last_case_id", "pending", "rc-txn-2026-07-118845-A-114", `"routes":["refund"]`} {
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
	graph, err := newGraph(reg)
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

// The live path must be able to answer a status question too. Without the
// check_refund_status tool the model has nothing to read with and refuses —
// the failure this test pins down.
func TestLiveAgentDispatchesStatusTool(t *testing.T) {
	reg := &refund.Registry{}
	// Seed the register as a refund turn would, so the read has something real
	// to find rather than a status the model could have written itself.
	seed, err := newGraph(reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := labrun.Run(t.Context(), seed, demoInput); err != nil {
		t.Fatal(err)
	}

	m := fakellm.New("scripted",
		fakellm.CallTurn("check_refund_status", map[string]any{"case_id": demoCaseID}),
		fakellm.TextTurn("Статус: pending."))
	a, err := newLiveAgent(m, reg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := labrun.Run(t.Context(), a, "check status "+demoCaseID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.CalledTool("check_refund_status") || m.Remaining() != 0 {
		t.Fatalf("status tool was not dispatched: %+v", res)
	}
	if !deltaCarries(res, refund.StateKeyCaseID, demoCaseID) {
		t.Fatalf("status turn left no case ID in the log: %+v", res.Events)
	}
	if len(res.ToolResults) == 0 {
		t.Fatal("the tool response never reached the event stream")
	}
}

// Both tools are declared on the live agent. A model that can only open cases
// invents statuses; this asserts the read tool is actually offered to it.
func TestLiveAgentDeclaresBothTools(t *testing.T) {
	m := fakellm.New("scripted", fakellm.TextTurn("нічого"))
	a, err := newLiveAgent(m, &refund.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := labrun.Run(t.Context(), a, "що ти вмієш?"); err != nil {
		t.Fatal(err)
	}
	// The request the model received carries the tool declarations, keyed by
	// tool name.
	if len(m.Requests()) == 0 {
		t.Fatal("no request reached the model")
	}
	for _, want := range []string{"open_refund_case", "check_refund_status"} {
		if _, ok := m.Requests()[0].Tools[want]; !ok {
			t.Fatalf("%s not declared; declared: %v", want, m.Requests()[0].Tools)
		}
	}
}
