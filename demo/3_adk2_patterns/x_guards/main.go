// X · Guards — anti-patterns that ADK Go refuses at build time.
//
// Each case wires a deliberately broken graph and prints the error
// workflow.New returns before a single model call is paid for. The last rows
// are the failures NO validator catches: those stay your discipline.
//
//	go run .            # the table of guards, offline
//	go run . console    # a correct graph: Start → fan-out → JoinNode → merge
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:    "X",
	Title: "Guards (anti-patterns caught at build time)",
	Input: "check",
	Build: build,
	Demo:  demo,
}

func main() { kit.Main(spec) }

func step(name string) workflow.Node {
	return workflow.NewFunctionNode(name, func(_ agent.Context, in any) (string, error) {
		return fmt.Sprintf("%s(%v)", name, in), nil
	}, workflow.NodeConfig{})
}

func echoBrain(p kit.Prompt) kit.Reply { return kit.Say("ok: %s", p.Text) }

// guardCase is one broken graph and the guard expected to reject it.
type guardCase struct {
	AntiPattern string
	// Sentinel is the exported error, when the guard has one.
	Sentinel error
	// Contains is matched when the guard has no sentinel.
	Contains string
	Edges    func(kit.Models) ([]workflow.Edge, error)
}

func cases() []guardCase {
	return []guardCase{
		{
			AntiPattern: "loop with no exit condition",
			Sentinel:    workflow.ErrUnconditionalCycle,
			Edges: func(kit.Models) ([]workflow.Edge, error) {
				a, b := step("poll"), step("wait")
				return workflow.NewEdgeBuilder().Add(workflow.Start, a).Add(a, b).Add(b, a).Build(), nil
			},
		},
		{
			AntiPattern: "merging branches without a JoinNode",
			Sentinel:    workflow.ErrUnsupportedFanIn,
			Edges: func(kit.Models) ([]workflow.Edge, error) {
				a, b, merge := step("research_a"), step("research_b"), step("merge")
				return workflow.NewEdgeBuilder().
					AddFanOut(workflow.Start, a, b).
					Add(a, merge).Add(b, merge).Build(), nil
			},
		},
		{
			AntiPattern: "two Default branches on one router",
			Sentinel:    workflow.ErrMultipleDefaultRoutes,
			Edges: func(kit.Models) ([]workflow.Edge, error) {
				r := step("router")
				return workflow.NewEdgeBuilder().
					Add(workflow.Start, r).
					AddRoute(r, step("desk_a"), workflow.Default).
					AddRoute(r, step("desk_b"), workflow.Default).Build(), nil
			},
		},
		{
			AntiPattern: "node nobody can reach",
			Sentinel:    workflow.ErrNodesNotReachable,
			Edges: func(kit.Models) ([]workflow.Edge, error) {
				return workflow.NewEdgeBuilder().
					Add(workflow.Start, step("a")).
					Add(step("orphan"), step("orphan_next")).Build(), nil
			},
		},
		{
			AntiPattern: "two different nodes with one name",
			Sentinel:    workflow.ErrDuplicateNodeName,
			Edges: func(kit.Models) ([]workflow.Edge, error) {
				return workflow.Chain(workflow.Start, step("step"), step("step")), nil
			},
		},
		{
			AntiPattern: "Task-mode agent as a static graph node",
			Contains:    "mode='task'",
			Edges: func(m kit.Models) ([]workflow.Edge, error) {
				return agentEdges(m, llmagent.ModeTask, false)
			},
		},
		{
			AntiPattern: "chat-mode agent fed by a predecessor node",
			Contains:    "mode='chat'",
			Edges: func(m kit.Models) ([]workflow.Edge, error) {
				return agentEdges(m, llmagent.ModeChat, true)
			},
		},
	}
}

// agentEdges places an llmagent of the given mode in a graph, optionally
// behind a function node.
func agentEdges(m kit.Models, mode llmagent.Mode, behindNode bool) ([]workflow.Edge, error) {
	a, err := llmagent.New(llmagent.Config{
		Name:        "helper",
		Description: "A helper agent.",
		Model:       m.For("helper", echoBrain),
		Instruction: "Help with the request.",
		Mode:        mode,
	})
	if err != nil {
		return nil, err
	}
	n, err := workflow.NewAgentNode(a, workflow.NodeConfig{})
	if err != nil {
		return nil, err
	}
	if behindNode {
		return workflow.Chain(workflow.Start, step("prepare"), n), nil
	}
	return workflow.Chain(workflow.Start, n), nil
}

// check builds the case's graph and reports the guard's error.
func check(m kit.Models, c guardCase) error {
	edges, err := c.Edges(m)
	if err != nil {
		return err
	}
	_, err = workflow.New("broken", edges)
	return err
}

// caught reports whether err is the guard the case expects.
func (c guardCase) caught(err error) bool {
	if err == nil {
		return false
	}
	if c.Sentinel != nil {
		return errors.Is(err, c.Sentinel)
	}
	return strings.Contains(err.Error(), c.Contains)
}

// uncaught are anti-patterns with no mechanical guard in ADK Go v2.5.0.
var uncaught = []string{
	"loop WITH a route but without an iteration cap → set a counter / MaxIterations yourself",
	"fan-out without a concurrency limit → workflow.WithMaxConcurrency(n) / NewParallelWorker(…, maxConcurrency, …)",
	"vague critic (\"review this\") → enumerated criteria + structured verdict (D1)",
	"side effect before a HITL pause → do it after resume (E1)",
}

func demo(_ context.Context, m kit.Models, w io.Writer) error {
	return runGuards(w, m, cases())
}

// runGuards prints one row per case and fails if any guard did not fire.
func runGuards(w io.Writer, m kit.Models, cs []guardCase) error {
	var missed []string
	for _, c := range cs {
		err := check(m, c)
		mark := "🛡️ "
		if !c.caught(err) {
			mark = "⚠️ "
			missed = append(missed, c.AntiPattern)
		}
		fmt.Fprintf(w, "%s %-44s → %v\n", mark, c.AntiPattern, firstLine(err))
	}
	fmt.Fprintln(w, "not caught by any validator — your discipline:")
	for _, u := range uncaught {
		fmt.Fprintf(w, "   · %s\n", u)
	}
	if len(missed) > 0 {
		return fmt.Errorf("guards did not fire for: %s", strings.Join(missed, "; "))
	}
	return nil
}

func firstLine(err error) string {
	if err == nil {
		return "<no error>"
	}
	s := err.Error()
	if i := strings.IndexAny(s, ".\n"); i > 0 && strings.Contains(s, "mode=") {
		return s[:i]
	}
	return s
}

// build returns the CORRECT counterpart: fan-out merged through a JoinNode.
//
// Note on the concurrency cap: workflowagent.New (v2.5.0) calls workflow.New
// with no options, so workflow.WithMaxConcurrency cannot reach a graph built
// through it. The cap is checked on a bare workflow.New below; inside a
// workflowagent, bound fan-out with NewParallelWorker's maxConcurrency.
func build(kit.Models) (agent.Agent, error) {
	a, b := step("research_a"), step("research_b")
	join := workflow.NewJoinNode("join")
	merge := workflow.NewFunctionNode("merge", func(_ agent.Context, in map[string]any) (string, error) {
		return fmt.Sprintf("merged %v + %v", in["research_a"], in["research_b"]), nil
	}, workflow.NodeConfig{})
	edges := workflow.NewEdgeBuilder().
		AddFanOut(workflow.Start, a, b).
		AddFanIn(join, a, b).
		Add(join, merge).
		Build()
	if _, err := workflow.New("guards_ok", edges, workflow.WithMaxConcurrency(2)); err != nil {
		return nil, err
	}
	return workflowagent.New(workflowagent.Config{
		Name:        "guards_ok",
		Description: "Correct fan-out: JoinNode merge.",
		Edges:       edges,
	})
}
