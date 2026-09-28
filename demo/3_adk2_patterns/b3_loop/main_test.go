package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

func TestLoop(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		wantPolls int
		want      string
	}{
		{"finishes on poll 3", "render report, needs 3 polls", 3, "Your job finished"},
		{"default needs 3", "render report", 3, "Your job finished"},
		{"first poll done", "needs 1", 1, "Your job finished"},
		{"cap stops a job that never ends", "render video, needs 9", maxPolls, "did not finish"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := build(kit.Offline())
			if err != nil {
				t.Fatal(err)
			}
			tr, err := kit.Run(context.Background(), a, io.Discard, tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if got := polls(tr); got != tt.wantPolls {
				t.Errorf("poll ran %d times, want %d", got, tt.wantPolls)
			}
			if !strings.Contains(tr.Final, tt.want) {
				t.Errorf("final = %q, want %q", tr.Final, tt.want)
			}
		})
	}
}

// polls counts the activations of the poll node in the trace.
func polls(tr kit.Trace) int {
	n := 0
	for _, ev := range tr.Events {
		if _, ok := ev.Output.(job); ok && len(ev.Routes) > 0 {
			n++
		}
	}
	return n
}

// TestUnconditionalCycleRejected is the mechanical half of the guard: the
// same loop without a route does not build.
func TestUnconditionalCycleRejected(t *testing.T) {
	t.Parallel()
	if err := unconditionalCycle(); !errors.Is(err, workflow.ErrUnconditionalCycle) {
		t.Fatalf("err = %v, want ErrUnconditionalCycle", err)
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "route=[false]") {
		t.Errorf("demo trace misses the back-edge route:\n%s", b.String())
	}
}
