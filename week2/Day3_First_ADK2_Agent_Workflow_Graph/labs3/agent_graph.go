package main

import (
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/week2/internal/refund"
)

// newGraph builds the model-free path: an explicit workflow graph where every
// call to `open_refund_case` or `check_refund_status` is a node of its own, so
// the event log shows the case being read or opened rather than a model
// claiming it was.
//
// The edges are spelled out here rather than hidden in the shared package, so
// this file is where you read — and change — the topology:
//
//	Start → classify ─┬─ "refund"        → prepare → open_refund_case → format
//	                  ├─ "status"        → prepare_status → check_refund_status → format_status
//	                  └─ "out_of_domain" → refuse
//
// There is no merge node: each branch is terminal, which is what the engine
// expects of a leaf. The step functions and the tools come from
// week2/internal/refund; this file only wires them together. Lab 4 composes the
// same steps for its REST service in its own agent_graph.go, so each lab owns
// its graph.
func newGraph(reg *refund.Registry) (agent.Agent, error) {
	refundTool, err := refund.NewTool(reg)
	if err != nil {
		return nil, fmt.Errorf("create refund tool: %w", err)
	}
	statusTool, err := refund.NewStatusTool(reg)
	if err != nil {
		return nil, fmt.Errorf("create status tool: %w", err)
	}
	// No retries: all work is local; validation errors cannot improve on retry.
	cfg := workflow.NodeConfig{}
	classify := workflow.NewEmittingFunctionNode("classify", classifyRoute, cfg)
	prepare := workflow.NewFunctionNode("prepare", refund.Prepare, cfg)
	openCase, err := workflow.NewToolNodeTyped[refund.Input, refund.Output](refundTool, cfg)
	if err != nil {
		return nil, fmt.Errorf("create refund node: %w", err)
	}
	format := workflow.NewFunctionNode("format", refund.Format, cfg)
	prepareStatus := workflow.NewFunctionNode("prepare_status", refund.PrepareStatus, cfg)
	checkStatus, err := workflow.NewToolNodeTyped[refund.StatusInput, refund.Output](statusTool, cfg)
	if err != nil {
		return nil, fmt.Errorf("create status node: %w", err)
	}
	formatStatus := workflow.NewFunctionNode("format_status", refund.FormatStatus, cfg)
	// The refusal is a declared step, not the absence of one: a request the
	// domain does not handle gets an answer in the log like every other.
	refuse := workflow.NewFunctionNode("refuse", refund.OutOfDomain, cfg)

	edges := workflow.Concat(
		workflow.Chain(workflow.Start, classify),
		[]workflow.Edge{
			{From: classify, To: prepare, Route: workflow.StringRoute(refund.RouteRefund)},
			{From: classify, To: prepareStatus, Route: workflow.StringRoute(refund.RouteStatus)},
			{From: classify, To: refuse, Route: workflow.StringRoute(refund.RouteOutOfDomain)},
		},
		workflow.Chain(prepare, openCase, format),
		workflow.Chain(prepareStatus, checkStatus, formatStatus),
	)
	return workflowagent.New(workflowagent.Config{
		Name:        refund.AppName,
		Description: "LEDGERWORKS: route the request, then open a refund case, read an existing one, or refuse.",
		Edges:       edges,
	})
}

// classifyRoute emits the routing event and returns nil, which suppresses the
// node's own terminal event: the route is the only thing this node is for, and
// a second event carrying the same string would say nothing new.
//
// Output carries the user message into every branch. Without it the successor
// would receive a nil input and see an empty request instead of the one that
// was actually asked.
func classifyRoute(ctx agent.Context, msg string, emit func(*session.Event) error) (any, error) {
	route, err := refund.Classify(ctx, msg)
	if err != nil {
		return nil, err
	}
	ev := session.NewEvent(ctx, ctx.InvocationID())
	ev.Routes = []string{route}
	ev.Output = msg
	if err := emit(ev); err != nil {
		return nil, err
	}
	return nil, nil
}
