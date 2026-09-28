package main

import (
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/week2/internal/refund"
)

// newGraph builds the model-free path: an explicit workflow graph where every
// call to `open_refund_case` is a node of its own, so the event log shows the
// case being opened rather than a model claiming it was.
//
// The edges are spelled out here rather than hidden in the shared package, so
// this file is where you read — and change — the topology:
//
//	Start → prepare → open_refund_case → format
//
// The step functions and the tool come from week2/internal/refund; this file
// only wires them together. Lab 4 composes the same steps for its REST service
// in its own agent_graph.go, so each lab owns its graph.
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
