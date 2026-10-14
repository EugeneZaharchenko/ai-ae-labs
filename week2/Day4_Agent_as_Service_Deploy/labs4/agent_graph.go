package main

import (
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/week2/internal/refund"
)

// newGraph is my agent from HW3 (newStaticGraph in labs3/agent_graph.go):
// Start → prepare → open_refund_case → format.
// Here it is not run in the console but served over HTTP by adkrest
// (see service.go). The name stays refund.AppName ("first_graph_agent"),
// because appName in every REST request must match it.
//
// HW4: I kept the plain chain without the classify router from labs3 —
// the service has one job, open a refund case, so every request goes
// through open_refund_case and the case is always opened by the tool node.
func newGraph(reg *refund.Registry) (agent.Agent, error) {
	refundTool, err := refund.NewTool(reg)
	if err != nil {
		return nil, fmt.Errorf("create refund tool: %w", err)
	}
	// no retries: everything is local, a bad request won't get better on retry
	cfg := workflow.NodeConfig{}
	prepare := workflow.NewFunctionNode("prepare", refund.Prepare, cfg)
	openCase, err := workflow.NewToolNodeTyped[refund.Input, refund.Output](refundTool, cfg)
	if err != nil {
		return nil, fmt.Errorf("create refund node: %w", err)
	}
	format := workflow.NewFunctionNode("format", refund.Format, cfg)
	return workflowagent.New(workflowagent.Config{
		Name:        refund.AppName,
		Description: "LEDGERWORKS (HW4 service): prepare the request, open the refund case, format the answer.",
		Edges:       workflow.Chain(workflow.Start, prepare, openCase, format),
	})
}
