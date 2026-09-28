package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
	"google.golang.org/adk/v2/workflow"
)

func TestGuardsFire(t *testing.T) {
	t.Parallel()
	for _, c := range cases() {
		t.Run(c.AntiPattern, func(t *testing.T) {
			t.Parallel()
			err := check(kit.Offline(), c)
			if err == nil {
				t.Fatal("graph was accepted; the guard did not fire")
			}
			if c.Sentinel != nil && !errors.Is(err, c.Sentinel) {
				t.Errorf("err = %v, want errors.Is %v", err, c.Sentinel)
			}
			if !c.caught(err) {
				t.Errorf("caught(%v) = false", err)
			}
		})
	}
}

func TestCaught(t *testing.T) {
	t.Parallel()
	sentinel := guardCase{Sentinel: workflow.ErrUnconditionalCycle}
	text := guardCase{Contains: "mode='task'"}
	tests := []struct {
		c    guardCase
		err  error
		want bool
	}{
		{sentinel, nil, false},
		{sentinel, errors.New("other"), false},
		{text, errors.New("Agent has mode='task'"), true},
	}
	for _, tt := range tests {
		if got := tt.c.caught(tt.err); got != tt.want {
			t.Errorf("caught(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestCorrectGraphRuns(t *testing.T) {
	t.Parallel()
	a, err := build(kit.Offline())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := kit.Run(context.Background(), a, io.Discard, "q")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tr.Final, "merged research_a(q) + research_b(q)") {
		t.Errorf("final = %q", tr.Final)
	}
}

func TestFirstLine(t *testing.T) {
	t.Parallel()
	if got := firstLine(nil); got != "<no error>" {
		t.Errorf("firstLine(nil) = %q", got)
	}
	if got := firstLine(errors.New("Agent has mode='task' and x. More.")); got != "Agent has mode='task' and x" {
		t.Errorf("firstLine = %q", got)
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "⚠️") {
		t.Errorf("a guard did not fire:\n%s", b.String())
	}
}

func TestRunGuardsReportsMissedGuard(t *testing.T) {
	t.Parallel()
	boom := errors.New("cannot build edges")
	cs := []guardCase{
		{AntiPattern: "valid graph", Sentinel: workflow.ErrUnconditionalCycle,
			Edges: func(kit.Models) ([]workflow.Edge, error) { return workflow.Chain(workflow.Start, step("a")), nil }},
		{AntiPattern: "edges error", Contains: "never",
			Edges: func(kit.Models) ([]workflow.Edge, error) { return nil, boom }},
	}
	var b strings.Builder
	err := runGuards(&b, kit.Offline(), cs)
	if err == nil || !strings.Contains(err.Error(), "valid graph; edges error") {
		t.Fatalf("err = %v, want both cases reported as missed", err)
	}
	if !strings.Contains(b.String(), "<no error>") {
		t.Errorf("row for the valid graph misses <no error>:\n%s", b.String())
	}
}

func TestEchoBrain(t *testing.T) {
	t.Parallel()
	if got := echoBrain(kit.Prompt{Text: "hi"}).Text; got != "ok: hi" {
		t.Errorf("echoBrain = %q", got)
	}
}
