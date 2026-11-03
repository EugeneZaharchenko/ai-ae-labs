package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/scripted"
	"github.com/dimetron/ai-eng-course/labs/internal/usage"
)

// The tests drive the loop directly rather than through a runner, and hand it a
// scripted model. Everything else — the request the loop builds, the parallel
// dispatch, the id correlation, the side-effect guard, the step cap — is the
// production code path. Nothing here needs a key or a network.

// newInvestigator takes the concrete scripted model rather than the interface,
// because the assertions need to see what the loop asked for.
func newInvestigator(llm *scripted.Model) (*investigator, *World, *usage.Meter) {
	world, meter := &World{}, &usage.Meter{}
	return &investigator{model: llm, world: world, meter: meter}, world, meter
}

func noEmit(*session.Event) error { return nil }

func TestLoopChainsToolsAndAnswers(t *testing.T) {
	llm := scriptedInvestigation()
	inv, world, meter := newInvestigator(llm)

	answer, err := inv.loop(context.Background(), "inv-1", defaultTask, noEmit)
	if err != nil {
		t.Fatalf("loop failed: %v", err)
	}

	// The answer has to name the root cause, not merely restate the complaint.
	if !strings.Contains(answer, "INC-418") {
		t.Errorf("answer does not name the incident:\n%s", answer)
	}

	// The effect is asserted on the world, not on the prose. A loop that talked
	// convincingly and opened nothing would fail here.
	tickets := world.OpenTickets()
	if len(tickets) != 1 {
		t.Fatalf("want exactly 1 ticket opened, got %d: %+v", len(tickets), tickets)
	}
	if tickets[0].Key != "LDG-1" {
		t.Errorf("want ticket key LDG-1, got %q", tickets[0].Key)
	}
	if !strings.Contains(tickets[0].Detail, "RB-12") {
		t.Errorf("ticket detail does not cite the runbook it followed: %q", tickets[0].Detail)
	}

	// Five steps of chaining: payouts → (batch + merchant) → wiki → ticket → answer.
	if got := llm.Calls(); got != 5 {
		t.Errorf("want 5 model calls, got %d", got)
	}
	if total := meter.Total(); total.Total() == 0 || total.Calls != 5 {
		t.Errorf("meter should have counted 5 calls' worth of tokens, got %+v", total)
	}
	for _, name := range []string{
		"get_merchant_payouts", "get_settlement_batch", "get_merchant",
		"search_internal_wiki", "create_jira_ticket",
	} {
		if inv.Counts()[name] != 1 {
			t.Errorf("want %s called once, got %d", name, inv.Counts()[name])
		}
	}
}

func TestLoopAllowsOneStateChangePerRun(t *testing.T) {
	// A model that has lost track of the conversation and asks for the same
	// ticket twice. The loop must refuse the second, not open two tickets.
	llm := scripted.New("forgetful",
		scripted.Call(10, scripted.FC("a", "create_jira_ticket", map[string]any{
			"summary": "A-114 held", "detail": "first attempt",
		})),
		scripted.Call(10, scripted.FC("b", "create_jira_ticket", map[string]any{
			"summary": "A-114 held", "detail": "second attempt",
		})),
		scripted.Say("done", 10),
	)
	inv, world, _ := newInvestigator(llm)

	if _, err := inv.loop(context.Background(), "inv-2", defaultTask, noEmit); err != nil {
		t.Fatalf("loop failed: %v", err)
	}

	tickets := world.OpenTickets()
	if len(tickets) != 1 {
		t.Fatalf("want exactly 1 ticket, got %d: %+v", len(tickets), tickets)
	}
	if tickets[0].Detail != "first attempt" {
		t.Errorf("the wrong call won: %q", tickets[0].Detail)
	}

	// The model was told why, rather than left to guess.
	refusal := lastResponseText(t, requestAfter(t, llm, 2))
	if !strings.Contains(refusal, "already made its one state-changing call") {
		t.Errorf("the refusal was not passed back to the model:\n%s", refusal)
	}
}

func TestLoopToolErrorIsAnObservation(t *testing.T) {
	// The claim in the code is that a tool error is an observation, not a crash.
	// So: an invented merchant id, then a recovery. This is the whole difference
	// between an agent loop and a pipeline, and it is worth a test.
	llm := scripted.New("invents-ids",
		scripted.Call(10, scripted.FC("a", "get_merchant_payouts", map[string]any{"merchant_id": "A-999"})),
		scripted.Call(10, scripted.FC("b", "get_merchant_payouts", map[string]any{"merchant_id": "A-114"})),
		scripted.Say("A-114 has two pending payouts.", 10),
	)
	inv, _, _ := newInvestigator(llm)

	answer, err := inv.loop(context.Background(), "inv-3", defaultTask, noEmit)
	if err != nil {
		t.Fatalf("a tool error should not end the run: %v", err)
	}
	if !strings.Contains(answer, "A-114 has two pending payouts") {
		t.Errorf("loop did not reach the answer after recovering: %q", answer)
	}

	observation := lastResponseText(t, requestAfter(t, llm, 1))
	if !strings.Contains(observation, "ERROR:") || !strings.Contains(observation, "A-999") {
		t.Errorf("the error did not reach the model as an observation:\n%s", observation)
	}
	// The message has to be actionable, or the model can only guess again.
	if !strings.Contains(observation, "A-114") {
		t.Errorf("the error does not say what a valid id looks like:\n%s", observation)
	}
}

func TestLoopCorrelatesParallelResultsByCallID(t *testing.T) {
	// Two calls in one turn is the normal case, and the results must come back
	// tagged with the id of the call each answers. Without the ids a provider
	// that batches calls cannot tell a result from its neighbour's, and the
	// mismatch is silent — the model simply sees the wrong evidence.
	llm := scripted.New("batches",
		scripted.Call(10,
			scripted.FC("c-batch", "get_settlement_batch", map[string]any{"batch_id": "422"}),
			scripted.FC("c-merchant", "get_merchant", map[string]any{"merchant_id": "A-114"})),
		scripted.Say("done", 10),
	)
	inv, _, _ := newInvestigator(llm)

	if _, err := inv.loop(context.Background(), "inv-4", defaultTask, noEmit); err != nil {
		t.Fatalf("loop failed: %v", err)
	}

	responses := functionResponses(t, requestAfter(t, llm, 1))
	if len(responses) != 2 {
		t.Fatalf("want 2 function responses in the follow-up turn, got %d", len(responses))
	}
	want := map[string]string{
		"c-batch":    "Batch #422",
		"c-merchant": "Kvitka Market",
	}
	for id, substr := range want {
		fr, ok := responses[id]
		if !ok {
			t.Fatalf("no response carries id %q", id)
		}
		got, _ := fr.Response["result"].(string)
		if !strings.Contains(got, substr) {
			t.Errorf("response %q carries the wrong result: want %q in %q", id, substr, got)
		}
	}
}

func TestLoopStepCapNamesTheCause(t *testing.T) {
	// A model that never stops asking for tools. The cap has to fire, and the
	// error has to say what it was doing — a bare "too many iterations" sends a
	// reader looking in the wrong place.
	llm := scripted.Forever("runaway", scripted.Call(10,
		scripted.FC("x", "get_settlement_batch", map[string]any{"batch_id": "418"})))
	inv, _, _ := newInvestigator(llm)

	_, err := inv.loop(context.Background(), "inv-5", defaultTask, noEmit)
	if !errors.Is(err, ErrStepCap) {
		t.Fatalf("want ErrStepCap, got %v", err)
	}
	if !strings.Contains(err.Error(), "get_settlement_batch") {
		t.Errorf("the cap error does not name the tool being looped on: %v", err)
	}
	if got := llm.Calls(); got != maxIterations {
		t.Errorf("want the loop to stop at %d model calls, got %d", maxIterations, got)
	}
}

func TestLoopReturnsAProviderFailure(t *testing.T) {
	llm := scripted.New("broken", scripted.Fail(errors.New("upstream 503")))
	inv, _, _ := newInvestigator(llm)

	_, err := inv.loop(context.Background(), "inv-6", defaultTask, noEmit)
	if err == nil {
		t.Fatal("want an error from a failing provider")
	}
	for _, want := range []string{"model call failed", "upstream 503", "step 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestInvokeUnknownToolListsTheRegistry(t *testing.T) {
	inv, _, _ := newInvestigator(scripted.New("none"))
	got := inv.invoke(&genai.FunctionCall{Name: "get_merchant_balance"})

	if !strings.Contains(got, "no such tool") {
		t.Fatalf("want a refusal, got %q", got)
	}
	// The model can only recover if it is told what does exist.
	for _, name := range []string{"search_internal_wiki", "get_merchant_payouts", "create_jira_ticket"} {
		if !strings.Contains(got, name) {
			t.Errorf("refusal does not offer %s: %q", name, got)
		}
	}
}

func TestBriefArgs(t *testing.T) {
	// Sorted, so the same call always renders the same way, and truncated, so a
	// ticket body cannot push the interesting part off the screen.
	got := briefArgs(map[string]any{
		"summary": "held",
		"detail":  strings.Repeat("x", 200),
	})
	if !strings.HasPrefix(got, `detail="`) {
		t.Errorf("args are not sorted by key: %q", got)
	}
	if !strings.Contains(got, "...") || len(got) > 120 {
		t.Errorf("long values are not truncated: %q", got)
	}
	if empty := briefArgs(nil); empty != "" {
		t.Errorf("want empty string for no args, got %q", empty)
	}
}

func TestRunCLIOfflineEndToEnd(t *testing.T) {
	// The whole path: flag parsing, the scripted model, the graph, the runner,
	// the progress events and the final report. This is the test that would have
	// caught the previous version of this example, whose "agent" picked tools by
	// index and never called a model at all.
	if code := runCLI([]string{"-offline"}); code != 0 {
		t.Fatalf("offline run exited %d, want 0", code)
	}
}

// requestAfter returns the request the model received carrying the results of
// the given 1-based turn. Requests are recorded in call order, so turn N's
// results land in request N.
func requestAfter(t *testing.T, llm *scripted.Model, turn int) *model.LLMRequest {
	t.Helper()
	reqs := llm.Requests()
	if turn >= len(reqs) {
		t.Fatalf("no request carries the results of turn %d (have %d requests)", turn, len(reqs))
	}
	return reqs[turn]
}

// lastResponseText returns the text of the final turn in a request — for a
// follow-up request, that is the tool results the loop fed back.
func lastResponseText(t *testing.T, req *model.LLMRequest) string {
	t.Helper()
	if len(req.Contents) == 0 {
		t.Fatal("request carries no contents")
	}
	var b strings.Builder
	for _, p := range req.Contents[len(req.Contents)-1].Parts {
		switch {
		case p.FunctionResponse != nil:
			s, _ := p.FunctionResponse.Response["result"].(string)
			b.WriteString(s)
			b.WriteByte('\n')
		case p.Text != "":
			b.WriteString(p.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// functionResponses indexes the final turn's function responses by call id.
func functionResponses(t *testing.T, req *model.LLMRequest) map[string]*genai.FunctionResponse {
	t.Helper()
	if len(req.Contents) == 0 {
		t.Fatal("request carries no contents")
	}
	out := map[string]*genai.FunctionResponse{}
	for _, p := range req.Contents[len(req.Contents)-1].Parts {
		if p.FunctionResponse != nil {
			out[p.FunctionResponse.ID] = p.FunctionResponse
		}
	}
	return out
}
