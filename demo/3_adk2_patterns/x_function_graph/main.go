// X · Function Graph — a whole workflow built from function nodes only. No
// LlmAgent, no model, no key: the graph engine alone gives you ordering,
// parallel branches, a barrier, typed routes and a bounded loop.
//
//	Start → parse_order ─┬→ price_check ─┐
//	                     ├→ stock_check ─┼→ checks (Join) → decide ─approve─→ reserve ⟲ backoff
//	                     └→ fraud_check ─┘                         │           ├done───→ ship
//	                                                               │           └give_up→ backorder
//	                                                               ├review──→ manual_review
//	                                                               └Default─→ reject
//
//	go run .                                   # approve path, reserve succeeds on attempt 3
//	go run . -input "order 7: 2x gpu 1800"     # fraud check → manual_review
//	go run . -input "order 9: 1x unicorn 10"   # out of stock → reject (Default)
//	go run . -input "order 5: 2x mouse 10"     # lock never frees → backorder after the cap
package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:    "X",
	Title: "Function Graph (no LLM)",
	Input: "order 1001: 3x keyboard 120",
	Build: build,
}

func main() { kit.Main(spec) }

// maxReserveAttempts is the hard cap of the reserve loop. The graph validator
// only guarantees the back edge is routed; the counter is ours to write.
const maxReserveAttempts = 5

// lockFreesOn simulates a warehouse lock per item: the attempt on which the
// reservation succeeds. 0 means the lock never frees, so the cap fires.
var lockFreesOn = map[string]int{"keyboard": 3, "gpu": 1, "mouse": 0}

// fraudLimit is the order total above which a human must look.
const fraudLimit = 1000

var stock = map[string]int{"keyboard": 10, "mouse": 25, "gpu": 4}

type order struct {
	ID    string `json:"id"`
	Qty   int    `json:"qty"`
	Item  string `json:"item"`
	Price int    `json:"price"`
}

// check carries the order along, so the step after the Join has it without
// reaching back past the barrier.
type check struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Note  string `json:"note"`
	Order order  `json:"order"`
}

type ticket struct {
	Order   order  `json:"order"`
	Verdict string `json:"verdict"`
	Attempt int    `json:"attempt"`
}

// parseOrder reads "order <id>: <qty>x <item> <unit price>".
func parseOrder(_ agent.Context, in string) (order, error) {
	head, body, ok := strings.Cut(in, ":")
	f := strings.Fields(body)
	if !ok || len(f) != 3 {
		return order{}, fmt.Errorf("want \"order <id>: <qty>x <item> <price>\", got %q", in)
	}
	qty, err := strconv.Atoi(strings.TrimSuffix(f[0], "x"))
	if err != nil {
		return order{}, fmt.Errorf("quantity %q: %w", f[0], err)
	}
	price, err := strconv.Atoi(f[2])
	if err != nil {
		return order{}, fmt.Errorf("price %q: %w", f[2], err)
	}
	return order{ID: strings.TrimSpace(strings.TrimPrefix(head, "order")), Qty: qty, Item: f[1], Price: price}, nil
}

func priceCheck(_ agent.Context, o order) (check, error) {
	return check{Name: "price", OK: o.Price > 0, Note: fmt.Sprintf("total %d", o.Qty*o.Price), Order: o}, nil
}

func stockCheck(_ agent.Context, o order) (check, error) {
	have := stock[o.Item]
	return check{Name: "stock", OK: have >= o.Qty, Note: fmt.Sprintf("have %d, need %d", have, o.Qty), Order: o}, nil
}

func fraudCheck(_ agent.Context, o order) (check, error) {
	total := o.Qty * o.Price
	return check{Name: "fraud", OK: total <= fraudLimit, Note: fmt.Sprintf("total %d vs limit %d", total, fraudLimit), Order: o}, nil
}

// decide reads the Join output — a map of predecessor name → output — and
// routes. An unknown verdict emits no route, so only Default matches.
func decide(ctx agent.Context, in map[string]any) (*session.Event, error) {
	checks := map[string]check{}
	var t ticket
	for name, v := range in {
		var c check
		if err := convert(v, &c); err != nil {
			return nil, fmt.Errorf("join input %s: %w", name, err)
		}
		checks[c.Name] = c
		t.Order = c.Order
	}
	ev := session.NewEvent(ctx, ctx.InvocationID())
	switch {
	case !checks["price"].OK || !checks["stock"].OK:
		t.Verdict = "reject: " + checks["stock"].Note // no route → Default
	case !checks["fraud"].OK:
		t.Verdict = "review: " + checks["fraud"].Note
		ev.Routes = []string{"review"}
	default:
		t.Verdict = "approve"
		ev.Routes = []string{"approve"}
	}
	ev.Output = t
	return ev, nil
}

// reserve is the loop body: each pass is one attempt, the route decides
// whether to go round again.
func reserve(ctx agent.Context, t ticket) (*session.Event, error) {
	t.Attempt++
	ev := session.NewEvent(ctx, ctx.InvocationID())
	switch {
	case lockFreesOn[t.Order.Item] > 0 && t.Attempt >= lockFreesOn[t.Order.Item]:
		ev.Routes = []string{"done"}
	case t.Attempt >= maxReserveAttempts:
		ev.Routes = []string{"give_up"}
	default:
		ev.Routes = []string{"retry"}
	}
	ev.Output = t
	return ev, nil
}

func backoff(_ agent.Context, t ticket) (ticket, error) { return t, nil }

func finish(label string) func(agent.Context, ticket) (string, error) {
	return func(_ agent.Context, t ticket) (string, error) {
		s := fmt.Sprintf("%s: order %s (%dx %s)", label, t.Order.ID, t.Order.Qty, t.Order.Item)
		if t.Attempt > 0 {
			s += fmt.Sprintf(" after %d reserve attempt(s)", t.Attempt)
		}
		if t.Verdict != "" && t.Verdict != "approve" {
			s += " — " + t.Verdict
		}
		return s, nil
	}
}

// convert copies a loosely typed Join value (struct or map) into out.
func convert(v any, out any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func build(kit.Models) (agent.Agent, error) {
	cfg := workflow.NodeConfig{}
	parse := workflow.NewFunctionNode("parse_order", parseOrder, cfg)
	price := workflow.NewFunctionNode("price_check", priceCheck, cfg)
	stockN := workflow.NewFunctionNode("stock_check", stockCheck, cfg)
	fraud := workflow.NewFunctionNode("fraud_check", fraudCheck, cfg)
	join := workflow.NewJoinNode("checks")
	decideN := workflow.NewFunctionNode("decide", decide, cfg)
	reserveN := workflow.NewFunctionNode("reserve", reserve, cfg)
	backoffN := workflow.NewFunctionNode("backoff", backoff, cfg)

	edges := workflow.NewEdgeBuilder().
		Add(workflow.Start, parse).
		AddFanOut(parse, price, stockN, fraud).
		AddFanIn(join, price, stockN, fraud).
		Add(join, decideN).
		AddRoute(decideN, reserveN, workflow.StringRoute("approve")).
		AddRoute(decideN, workflow.NewFunctionNode("manual_review", finish("manual review"), cfg), workflow.StringRoute("review")).
		AddRoute(decideN, workflow.NewFunctionNode("reject", finish("rejected"), cfg), workflow.Default).
		// The loop: reserve → backoff → reserve. Legal only because the
		// back edge carries a route; all-unconditional would be
		// ErrUnconditionalCycle at build time.
		AddRoute(reserveN, backoffN, workflow.StringRoute("retry")).
		Add(backoffN, reserveN).
		AddRoute(reserveN, workflow.NewFunctionNode("ship", finish("shipped"), cfg), workflow.StringRoute("done")).
		AddRoute(reserveN, workflow.NewFunctionNode("backorder", finish("backordered"), cfg), workflow.StringRoute("give_up")).
		Build()

	return workflowagent.New(workflowagent.Config{
		Name:        "order_graph",
		Description: "Order processing from function nodes only — no model.",
		Edges:       edges,
	})
}
