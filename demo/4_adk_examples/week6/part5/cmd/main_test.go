package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedispatchesOnlyTheFailedWorker(t *testing.T) {
	var out bytes.Buffer
	if code := runCLI(nil, &out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	s := out.String()
	for _, want := range []string{"plan: 3 subtasks", "T2 [tests] attempt 1: gate failed", "T2 [tests] attempt 2: green", "READY FOR HUMAN REVIEW (not merged): 3/3"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "T1 [backend] attempt 2") {
		t.Errorf("a green worker must not be re-run:\n%s", s)
	}
}

func TestNoSecondChanceStopsAndNamesTheCause(t *testing.T) {
	var out bytes.Buffer
	if code := runCLI([]string{"-max-redispatch=0"}, &out); code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "T2 [tests] still red") || strings.Contains(out.String(), "READY FOR HUMAN REVIEW") {
		t.Fatalf("must stop with the named worker and never report ready:\n%s", out.String())
	}
}

func TestParsePlanRejects(t *testing.T) {
	for name, in := range map[string]string{
		"prose":     "Sure! Here is a plan.",
		"empty":     "[]",
		"unknown":   `[{"id":"T1","role":"deployer","goal":"ship it"}]`,
		"duplicate": `[{"id":"T1","role":"docs","goal":"a"},{"id":"T1","role":"docs","goal":"b"}]`,
	} {
		if _, err := parsePlan(in); err == nil {
			t.Errorf("%s: plan must be rejected", name)
		}
	}
}
