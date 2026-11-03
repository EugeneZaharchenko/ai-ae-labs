package main

import (
	"bytes"
	"strings"
	"testing"
)

// Two contours, as in week 4 part 2. The first tests the pure layer — the
// write policy and the fold — with no ADK in the room. The second runs the
// whole scenario through a real runner, session service and memory service
// with the offline stand-in, which is what pins the ADK facts the demo rests
// on: that ctx.State().Set produces a StateDelta, that user: state crosses
// sessions, and that temp: does not outlive its invocation.

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		in   FactInput
		ok   bool
	}{
		{"the day1 fact", FactInput{"integration.A-114", "legacy"}, true},
		{"family outside the allowlist", FactInput{"is_admin.A-114", "true"}, false},
		{"no merchant scope", FactInput{"integration", "legacy"}, false},
		{"malformed merchant", FactInput{"integration.a114", "legacy"}, false},
		{"placeholder value", FactInput{"integration.A-114", "undefined"}, false},
		{"empty value", FactInput{"integration.A-114", " "}, false},
		{"a paragraph is not a fact", FactInput{"integration.A-114", strings.Repeat("x", 81)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.in.Validate(); (err == nil) != tt.ok {
				t.Errorf("Validate(%+v) = %v, want ok=%v", tt.in, err, tt.ok)
			}
		})
	}
}

func TestDecide(t *testing.T) {
	poison := FactInput{"integration.A-114", "new-api"}

	if got := Decide(poison, "legacy", true, true, true); got.Status != "rejected" || !strings.Contains(got.Reason, "foreign text") {
		t.Errorf("a tainted write under the guard must be refused and say why: %+v", got)
	}
	// The incident: the same write with the guard off goes through.
	if got := Decide(poison, "legacy", true, true, false); got.Status != "accepted" {
		t.Errorf("guard=false is the policy the incident ran under: %+v", got)
	}
	if got := Decide(FactInput{"integration.A-114", "legacy"}, "legacy", true, false, true); got.Status != "unchanged" {
		t.Errorf("rewriting the same value is not a new event: %+v", got)
	}
	// Validation outranks trust: a malformed key is refused for its shape even
	// when the guard would also refuse it, so the reason names the real fault.
	if got := Decide(FactInput{"is_admin.A-114", "true"}, "", false, true, true); !strings.Contains(got.Reason, "family") {
		t.Errorf("want the allowlist named: %+v", got)
	}
}

func TestFoldIsFlatLastWriterWinsAndDropsTemp(t *testing.T) {
	got := Fold([]map[string]any{
		{"user:fact.integration.A-114": "legacy", "temp:untrusted_input": true},
		{"user:profile": map[string]any{"tz": "Europe/Kyiv", "lang": "uk"}},
		{"user:profile": map[string]any{"tz": "UTC"}},
	})
	if _, ok := got[TaintKey]; ok {
		t.Error("temp: keys are never persisted, so a replay must not resurrect them")
	}
	// Flat, not deep: the second profile REPLACES the first. A replay that
	// deep-merged would report a "lang" the stored state does not have.
	p, _ := got["user:profile"].(map[string]any)
	if _, ok := p["lang"]; ok || p["tz"] != "UTC" {
		t.Errorf("want a whole-value replace, got %v", p)
	}
	if got["user:fact.integration.A-114"] != "legacy" {
		t.Errorf("fact lost: %v", got)
	}
}

func TestRenderFactsIsStable(t *testing.T) {
	a := RenderFacts(map[string]string{"integration.A-114": "legacy", "contact_channel.A-114": "email"})
	b := RenderFacts(map[string]string{"contact_channel.A-114": "email", "integration.A-114": "legacy"})
	if a != b {
		t.Error("the same state must produce the same prompt")
	}
	if !strings.Contains(RenderFacts(nil), "none yet") {
		t.Error("an empty facts block has to say it is empty")
	}
}

func TestCLIOfflineRemembersAcrossSessionsAndRefusesThePoison(t *testing.T) {
	var out bytes.Buffer
	code := runCLI([]string{"-offline"}, &out)
	report := out.String()
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, report)
	}
	for _, want := range []string{
		// Layer 1: the write is a StateDelta on the tool's event.
		"[ev:state_delta] user:fact.integration.A-114 legacy [day1, memory_agent]",
		// day2 is a new session; the fact arrived through user: state.
		"For A-114 the integration is legacy — recommend the legacy fix",
		// The poisoned write is refused and recorded, though it changed nothing.
		"[ev:rejected] user:fact.integration.A-114",
		"replay: fold of 1 deltas == user state",
		"- integration.A-114 = legacy",
		"fact writes: 1 accepted, 1 rejected",
		"no usage reported",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q\n%s", want, report)
		}
	}
	// Layer 2: day4's load_memory finds the earlier conversations.
	if strings.Contains(report, "(0 past-conversation snippets") {
		t.Errorf("AddSessionToMemory was not called, or load_memory searched nothing:\n%s", report)
	}
	if strings.Contains(report, "incident:") {
		t.Errorf("the guarded run must not report the incident:\n%s", report)
	}
}

func TestCLIOfflineWithoutTheGuardReproducesTheIncident(t *testing.T) {
	var out bytes.Buffer
	code := runCLI([]string{"-offline", "-guard=false"}, &out)
	report := out.String()
	if code != 1 {
		t.Fatalf("exit %d, want 1 — a rewritten fact must be visible to a shell\n%s", code, report)
	}
	for _, want := range []string{
		"[ev:state_delta] user:fact.integration.A-114 new-api [day3, memory_agent]",
		"incident: integration.A-114 is \"new-api\"",
		"recommend migrating to the new-api",
		// The fold still matches: event sourcing records the poison faithfully.
		// It is an audit, not a defence.
		"replay: fold of 2 deltas == user state",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q\n%s", want, report)
		}
	}
}

func TestCLIRejectsABadFlag(t *testing.T) {
	var out bytes.Buffer
	if code := runCLI([]string{"-nope"}, &out); code != 2 {
		t.Errorf("want exit 2, got %d", code)
	}
	if out.Len() != 0 {
		t.Errorf("nothing belongs on stdout when the command line is wrong: %q", out.String())
	}
}
