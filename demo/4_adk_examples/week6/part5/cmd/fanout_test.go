package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/internal/adkrun"
)

// Separate RunNode calls inside one dynamic node run one after another. A
// ParallelWorker dispatched through ONE RunNode does fan out: the wrapped node
// runs concurrently for each item of the list. Four 300 ms items finishing well
// under 4×300 ms is the proof; the bound is loose so a busy CI box stays green.
func TestParallelWorkerFansOutInsideDynamicNode(t *testing.T) {
	const item = 300 * time.Millisecond
	slow := workflow.NewFunctionNode("slow", func(_ agent.Context, in string) (string, error) {
		time.Sleep(item)
		return "done-" + in, nil
	}, workflow.NodeConfig{})
	fan, err := workflow.NewParallelWorker("fan", slow, 0, workflow.NodeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	coord := workflow.NewDynamicNode[string, string]("coord", func(nc agent.Context, _ string, _ func(*session.Event) error) (string, error) {
		out, err := workflow.RunNode[[]any](nc, fan, []any{"a", "b", "c", "d"})
		return fmt.Sprint(out), err
	}, workflow.NodeConfig{})
	wf, err := adkrun.Pipeline("fanout", "ParallelWorker inside a dynamic node.", coord)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	out, err := adkrun.Run(context.Background(), wf, "go", nil)
	elapsed := time.Since(start)
	if err != nil || out != "[done-a done-b done-c done-d]" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if elapsed > 3*item {
		t.Fatalf("4 items took %v; ParallelWorker should overlap them (sequential would be ~%v)", elapsed, 4*item)
	}
}

// The wrapped node may not carry its own RetryConfig: retries belong to the
// ParallelWorker and run per item. ADK rejects the misconfiguration at build time.
func TestParallelWorkerRejectsRetryOnWrappedNode(t *testing.T) {
	n := workflow.NewFunctionNode("n", func(_ agent.Context, in string) (string, error) { return in, nil },
		workflow.NodeConfig{RetryConfig: workflow.DefaultRetryConfig()})
	if _, err := workflow.NewParallelWorker("fan", n, 2, workflow.NodeConfig{}); err == nil {
		t.Fatal("expected an error for RetryConfig on the wrapped node")
	}
}
