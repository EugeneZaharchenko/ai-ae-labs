package main

import (
	"fmt"
	"log"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/week2/internal/refund"
)

// newGraph builds the default path: an explicit workflow graph where every
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
//
// The classifier is the one knob: refund.Classify by default, a model when a
// Registry carries one (see RefundClassifier and `-classify`). Everything
// downstream is unaffected, because the classifier only produces the route
// string the edges already match on.
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
	var classify *workflow.FunctionNode
	if c := reg.ClassifierModel(); c != nil {
		classify = workflow.NewEmittingFunctionNode("classify", classifyRouteModel(c), cfg)
	} else {
		classify = workflow.NewEmittingFunctionNode("classify", classifyRouteText, cfg)
	}
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

// classifyRouteText emits the routing event and returns nil, which suppresses
// the node's own terminal event: the route is the only thing this node is for,
// and a second event carrying the same string would say nothing new.
//
// Output carries the user message into every branch. Without it the successor
// would receive a nil input and see an empty request instead of the one that
// was actually asked.
//
// The name says how the decision is made, not what it produces: the sibling
// classifyRouteModel produces the same event from the same vocabulary, and the
// two are interchangeable at the use site in newGraph.
func classifyRouteText(ctx agent.Context, msg string, emit func(*session.Event) error) (any, error) {
	route, err := refund.Classify(ctx, msg)
	if err != nil {
		return nil, err
	}
	log.Printf("classify: rule %q → %s", msg, route)
	ev := session.NewEvent(ctx, ctx.InvocationID())
	ev.Routes = []string{route}
	ev.Output = msg
	if err := emit(ev); err != nil {
		return nil, err
	}
	return nil, nil
}

// routeInstruction is the whole prompt of the model classifier: one of three
// route names, nothing else. Ukrainian, because the request is read in
// Ukrainian and a language switch here is one more thing to be wrong.
const routeInstruction = `Ти маршрутизатор запитів LEDGERWORKS. Відповідай рівно одним словом:
refund — просять відкрити кейс повернення;
status — питають про вже відкритий кейс;
out_of_domain — усе інше.`

// classifyRouteModel is the alternative classifier: the same node body as
// classifyRouteText — same event, same route vocabulary — decided by a model
// instead of by refund.Classify. It is what `-classify=model` selects
// (main.go), and it is why that flag needs a provider: this path calls one on
// every request, and its route is not reproducible — one wording may land on a
// different branch, which is the property refund.Classify gives up to buy
// testability. Temperature 0 narrows that gap, it does not close it.
//
// The classifier arrives as an argument rather than from a package-level
// variable, so two graphs in one process (a test's, and the same test's scripted
// replacement) cannot see each other's model.
func classifyRouteModel(c refund.Classifier) func(agent.Context, string, func(*session.Event) error) (any, error) {
	return func(ctx agent.Context, msg string, emit func(*session.Event) error) (any, error) {
		route, err := classifyWithModel(ctx, c, msg)
		if err != nil {
			return nil, err
		}
		// Console log beside the event, not instead of it: the event is the
		// audit trail a test asserts on, this line is what a human watches to
		// see which branch the model picked while the graph runs.
		log.Printf("classify: model %q → %s", msg, route)
		ev := session.NewEvent(ctx, ctx.InvocationID())
		ev.Routes = []string{route}
		ev.Output = msg
		if err := emit(ev); err != nil {
			return nil, err
		}
		return nil, nil
	}
}

// classifyWithModel asks for one route name and normalises the answer, never
// trusting it: a reply that is a whole sentence still lands on a branch the
// graph knows, because an unrecognised answer becomes out_of_domain rather than
// no route at all, which would dead-end the edge set above classify.
func classifyWithModel(ctx agent.Context, c refund.Classifier, msg string) (string, error) {
	req := &model.LLMRequest{
		Model:    c.Name(),
		Contents: []*genai.Content{genai.NewContentFromText(msg, genai.RoleUser)},
		Config: &genai.GenerateContentConfig{
			// A system turn: the user message must stay the only user content,
			// or the classifier sees a prompt that looks like the request it is
			// supposed to label.
			SystemInstruction: genai.NewContentFromText(routeInstruction, genai.RoleUser),
			// 0, so one wording keeps one branch and the log stays auditable.
			Temperature: genai.Ptr(float32(0)),
		},
	}
	var answer strings.Builder
	for resp, err := range c.GenerateContent(ctx, req, false) {
		if err != nil {
			return "", fmt.Errorf("classify with model: %w", err)
		}
		if resp.Content == nil {
			continue
		}
		for _, part := range resp.Content.Parts {
			answer.WriteString(part.Text)
		}
	}
	lowered := strings.ToLower(answer.String())
	// The specific names first, the catch-all last: RouteOutOfDomain nests no
	// other name today, so the order only matters for the names themselves.
	for _, route := range []string{refund.RouteRefund, refund.RouteStatus} {
		if strings.Contains(lowered, route) {
			return route, nil
		}
	}
	// Anything else — a paraphrase, a refusal, an empty response — is out of
	// domain, which is a branch that exists. Never return "": no route at all
	// would leave classify's edges with nothing to match.
	return refund.RouteOutOfDomain, nil
}
