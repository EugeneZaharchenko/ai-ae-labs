// Package examples holds minimal ADK Go v2.4.0 workflow snippets for the
// adk-go-workflow skill. Full runnable versions: demo/3_adk2_patterns/.
package examples

import (
	"encoding/json"
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
)

// Function-only graph: no model, no key.
//
//	Start → parse ─┬→ stock ─┐
//	               └→ fraud ─┴→ checks (Join) → decide ─approve→ reserve ⟲ backoff
//	                                                   │         └done→ ship
//	                                                   └Default→ reject

const maxAttempts = 5

type check struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Order string `json:"order"` // carried along: the Join sees only branch outputs
}

type ticket struct {
	Order   string `json:"order"`
	Attempt int    `json:"attempt"`
}

// NewFunctionGraph wires the graph above.
func NewFunctionGraph() (agent.Agent, error) {
	cfg := workflow.NodeConfig{}
	parse := workflow.NewFunctionNode("parse", func(_ agent.Context, in string) (string, error) {
		return in, nil
	}, cfg)
	stock := workflow.NewFunctionNode("stock", func(_ agent.Context, o string) (check, error) {
		return check{Name: "stock", OK: true, Order: o}, nil
	}, cfg)
	fraud := workflow.NewFunctionNode("fraud", func(_ agent.Context, o string) (check, error) {
		return check{Name: "fraud", OK: len(o) < 100, Order: o}, nil
	}, cfg)
	join := workflow.NewJoinNode("checks")

	// decide routes by setting ev.Routes. No route → only Default matches.
	decide := workflow.NewFunctionNode("decide", func(ctx agent.Context, in map[string]any) (*session.Event, error) {
		ev := session.NewEvent(ctx, ctx.InvocationID())
		ok := true
		var t ticket
		for name, v := range in { // Join output: predecessor name → output
			var c check
			if err := convert(v, &c); err != nil {
				return nil, fmt.Errorf("join input %s: %w", name, err)
			}
			ok = ok && c.OK
			t.Order = c.Order
		}
		if ok {
			ev.Routes = []string{"approve"}
		}
		ev.Output = t
		return ev, nil
	}, cfg)

	// reserve is the loop body; the counter is ours — the validator only
	// checks that the back edge carries a route.
	reserve := workflow.NewFunctionNode("reserve", func(ctx agent.Context, t ticket) (*session.Event, error) {
		t.Attempt++
		ev := session.NewEvent(ctx, ctx.InvocationID())
		ev.Output = t
		switch {
		case lockFree(t):
			ev.Routes = []string{"done"}
		case t.Attempt >= maxAttempts:
			ev.Routes = []string{"give_up"}
		default:
			ev.Routes = []string{"retry"}
		}
		return ev, nil
	}, cfg)
	backoff := workflow.NewFunctionNode("backoff", func(_ agent.Context, t ticket) (ticket, error) { return t, nil }, cfg)
	end := func(label string) workflow.Node {
		return workflow.NewFunctionNode(label, func(_ agent.Context, t ticket) (string, error) {
			return fmt.Sprintf("%s: %s after %d attempt(s)", label, t.Order, t.Attempt), nil
		}, cfg)
	}

	edges := workflow.NewEdgeBuilder().
		Add(workflow.Start, parse).
		AddFanOut(parse, stock, fraud).
		AddFanIn(join, stock, fraud). // fan-in only through a JoinNode
		Add(join, decide).
		AddRoute(decide, reserve, workflow.StringRoute("approve")).
		AddRoute(decide, end("reject"), workflow.Default). // exactly one Default
		AddRoute(reserve, backoff, workflow.StringRoute("retry")).
		Add(backoff, reserve). // legal: the cycle has a routed edge
		AddRoute(reserve, end("ship"), workflow.StringRoute("done")).
		AddRoute(reserve, end("backorder"), workflow.StringRoute("give_up")).
		Build()

	return workflowagent.New(workflowagent.Config{Name: "order_graph", Edges: edges})
}

// lockFree simulates a warehouse lock that frees on attempt 3 — except for an
// empty order, whose lock never frees, so the cap (give_up) is reachable.
func lockFree(t ticket) bool { return t.Order != "" && t.Attempt >= 3 }

// convert copies a loosely typed Join value into out.
func convert(v, out any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
