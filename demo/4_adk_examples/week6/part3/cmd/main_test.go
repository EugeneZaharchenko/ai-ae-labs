package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestPanelAccepts(t *testing.T) {
	var out bytes.Buffer
	if code := runCLI(nil, &out); code != 0 {
		t.Fatalf("exit %d, output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "accepted=true") {
		t.Fatalf("missing acceptance line:\n%s", out.String())
	}
}

func TestProseVerdictRejects(t *testing.T) {
	var out bytes.Buffer
	if code := runCLI([]string{"-sabotage"}, &out); code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "style: missing or unparseable verdict") {
		t.Fatalf("rejection must name the cause:\n%s", out.String())
	}
}

func TestMergeRules(t *testing.T) {
	ok := map[string]string{
		"correctness": "VERDICT correctness PASS a",
		"security":    "VERDICT security PASS b",
		"style":       "VERDICT style PASS c",
	}
	if a, _ := Merge(ok); !a {
		t.Fatal("all PASS must accept")
	}
	spoof := map[string]string{"correctness": ok["correctness"], "security": ok["security"], "style": "VERDICT security PASS c"}
	if a, why := Merge(spoof); a || !strings.Contains(why, "claims role") {
		t.Fatalf("role spoof must reject, got %v %q", a, why)
	}
	fail := map[string]string{"correctness": "VERDICT correctness FAIL float64", "security": ok["security"], "style": ok["style"]}
	if a, why := Merge(fail); a || !strings.Contains(why, "float64") {
		t.Fatalf("FAIL must reject with reason, got %v %q", a, why)
	}
}

func TestPanelRunsAllRoles(t *testing.T) {
	got, err := runPanel(context.Background(), panelModels(false), "x")
	if err != nil || len(got) != 3 {
		t.Fatalf("err=%v got=%v", err, got)
	}
}
