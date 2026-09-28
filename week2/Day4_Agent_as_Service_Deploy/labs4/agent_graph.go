package main

import (
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/week2/internal/refund"
)

// newGraph composes the refund workflow this service serves over REST. It is
// the same `Start → prepare → open_refund_case → format` shape Lab 3 builds in
// its own agent_graph.go, repeated here on purpose: this lab owns its
// topology, so Day 4 can extend the flow (draining, readiness, a second node)
// without changing what Day 3 hands in.
//
// The steps and the tool live in week2/internal/refund, which both labs share.
func newGraph(reg *refund.Registry) (agent.Agent, error) {
	refundTool, err := refund.NewTool(reg)
	if err != nil {
		return nil, fmt.Errorf("create refund tool: %w", err)
	}
	// No retries: all work is local; validation errors cannot improve on retry.
	cfg := workflow.NodeConfig{}
	prepare := workflow.NewFunctionNode("prepare", refund.Prepare, cfg)
	openCase, err := workflow.NewToolNodeTyped[refund.Input, refund.Output](refundTool, cfg)
	if err != nil {
		return nil, fmt.Errorf("create refund node: %w", err)
	}
	format := workflow.NewFunctionNode("format", refund.Format, cfg)
	return workflowagent.New(workflowagent.Config{
		Name:        refund.AppName,
		Description: "LEDGERWORKS: prepare a refund request, open its case, format the result.",
		Edges:       workflow.Chain(workflow.Start, prepare, openCase, format),
	})
}
