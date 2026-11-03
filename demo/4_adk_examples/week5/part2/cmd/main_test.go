package main

import (
	"bytes"
	"strings"
	"testing"
)

// Two contours. The pure layer — merge, hash, decision, the send rule — is
// tested on plain values. The CLI runs are end-to-end through a real
// workflowagent with a real pause and a real resume, and they pin what only a
// run can show: that the answer reaches the gate, that the gate is the only
// node re-run, and that a failed desk becomes a named gap rather than a
// failed graph.

func joinedAll() map[string]any {
	return map[string]any{
		"plan":             Plan{Task: "t", Desks: desks},
		"risk_desk":        Section{Desk: "risk_desk", Text: "- r"},
		"compliance_desk":  Section{Desk: "compliance_desk", Text: "- c"},
		"integration_desk": Section{Desk: "integration_desk", Text: "- i"},
	}
}

func TestMergeOrdersByCatalogAndIsStable(t *testing.T) {
	a := Merge(Plan{Task: "t", Desks: desks}, joinedAll())
	b := Merge(Plan{Task: "t", Desks: desks}, joinedAll())
	if a.Hash != b.Hash || a.Count != 3 || len(a.Gaps) != 0 {
		t.Fatalf("same input, same document: %+v vs %+v", a, b)
	}
	if strings.Index(a.Text, "risk_desk") > strings.Index(a.Text, "compliance_desk") {
		t.Errorf("sections out of catalog order:\n%s", a.Text)
	}
}

func TestMergeNamesEveryGap(t *testing.T) {
	j := joinedAll()
	j["compliance_desk"] = Section{Desk: "compliance_desk", Gap: "503"}
	delete(j, "integration_desk")
	b := Merge(Plan{Task: "t", Desks: desks}, j)
	if b.Count != 1 || len(b.Gaps) != 2 {
		t.Fatalf("want 1 section and 2 named gaps, got %+v", b)
	}
	if !strings.Contains(b.Text, "GAP: 503") || !strings.Contains(b.Text, "GAP: no section arrived") {
		t.Errorf("a gap must be visible in the document the human approves:\n%s", b.Text)
	}
}

func TestDecodeDecisionNeverReadsGarbageAsYes(t *testing.T) {
	if _, err := DecodeDecision("yes"); err == nil {
		t.Error("a bare string is not a decision")
	}
	if _, err := DecodeDecision(map[string]any{"approved": true}); err == nil {
		t.Error("an approval that names no approver is not an approval")
	}
	d, err := DecodeDecision(map[string]any{"approved": true, "by": "ops", "hash": "abc"})
	if err != nil || !d.Approved || d.Hash != "abc" {
		t.Errorf("valid decision misread: %+v, %v", d, err)
	}
}

func TestSendableBindsTheApprovalToTheDocument(t *testing.T) {
	b := Merge(Plan{Task: "t", Desks: desks}, joinedAll())
	ok := Outcome{Brief: b, Decision: Decision{Approved: true, By: "ops", Hash: b.Hash}}
	if err := ok.Sendable(); err != nil {
		t.Errorf("approved, same document: %v", err)
	}
	stale := Outcome{Brief: b, Decision: Decision{Approved: true, By: "ops", Hash: "000000000000"}}
	if err := stale.Sendable(); err == nil || !strings.Contains(err.Error(), "not "+b.Hash) {
		t.Errorf("a yes for another document must not send this one: %v", err)
	}
	no := Outcome{Brief: b, Decision: Decision{Approved: false, By: "ops", Note: "not yet", Hash: b.Hash}}
	if err := no.Sendable(); err == nil {
		t.Error("a rejection must not send")
	}
}

func TestCLIOfflinePausesResumesAndSendsOnce(t *testing.T) {
	var out bytes.Buffer
	code := runCLI([]string{"-offline"}, &out)
	report := out.String()
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, report)
	}
	for _, want := range []string{
		"paused at approval_gate (interrupt board-approval-",
		// Each section came from its own desk's sheet.
		"Merchant A-114 is on the legacy integration",
		"PCI DSS cardholder-data scope",
		"resumed: Iryna Datsiuk (VP Engineering) → approve",
		"dispatch: sent brief",
		// The resume proof: the desks ran once, not twice.
		"desk runs: risk_desk×1, compliance_desk×1, integration_desk×1",
		"gate: 1 question(s) asked, 1 answer(s) received",
		"briefs sent: 1",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q\n%s", want, report)
		}
	}
}

func TestCLIOfflineRejectionSendsNothingAndSaysWhy(t *testing.T) {
	var out bytes.Buffer
	code := runCLI([]string{"-offline", "-decision", "reject"}, &out)
	report := out.String()
	if code != 0 {
		t.Fatalf("a rejection is an outcome, not a failure: exit %d\n%s", code, report)
	}
	if !strings.Contains(report, "dispatch: not sent: rejected by Iryna Datsiuk") ||
		!strings.Contains(report, "briefs sent: 0 — dispatch refused it") {
		t.Errorf("the zero must name its cause:\n%s", report)
	}
}

func TestCLIOfflineContainsAFailedDesk(t *testing.T) {
	var out bytes.Buffer
	code := runCLI([]string{"-offline", "-fail-desk", "compliance_desk"}, &out)
	report := out.String()
	if code != 0 {
		t.Fatalf("one failed desk must not fail the graph: exit %d\n%s", code, report)
	}
	if !strings.Contains(report, "GAP: the desk's data source returned 503") {
		t.Errorf("the human must see the gap before approving:\n%s", report)
	}
	if !strings.Contains(report, "Merchant A-114 is on the legacy integration") {
		t.Errorf("the other desks' sections must survive:\n%s", report)
	}
}

func TestCLIRejectsBadFlags(t *testing.T) {
	for _, args := range [][]string{{"-decision", "maybe"}, {"-fail-desk", "nope"}, {"-nope"}} {
		var out bytes.Buffer
		if code := runCLI(args, &out); code != 2 || out.Len() != 0 {
			t.Errorf("%v: want exit 2 and empty stdout, got %d %q", args, code, out.String())
		}
	}
}
