// The same loop in an ADK workflow graph. One step is a node (NewFunctionNode),
// the loop is a dynamic node (NewDynamicNode) that calls the step via RunNode.
package main

import (
	"fmt"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/react"
)

// StepResult is what the step node writes to the event log.
type StepResult struct {
	Iteration int              `json:"iteration"`
	Thought   string           `json:"thought"`
	Calls     int              `json:"calls"`
	Final     bool             `json:"final"`
	Answer    string           `json:"answer,omitempty"`
	ToolCalls []react.ToolCall `json:"tool_calls,omitempty"`
	Usage     react.Usage      `json:"usage"`
	// Raw is the model turn; some providers need it replayed with the results.
	// It stays out of the event log, RunNode hands the value over in memory.
	Raw *genai.Content `json:"-"`
}

// planInput is the input of plan_step. The history is passed in, not kept in a
// closure variable, so two sessions of the same agent do not share it.
type planInput struct {
	Iteration int
	History   react.History
}

// NewGraphAgent builds Start → react_loop. Inside react_loop every iteration
// is two child nodes: plan_step (Thought) and call_tools (Action + Observation).
func NewGraphAgent(p react.Planner, reg react.Registry, b react.Budget) (agent.Agent, error) {
	b = b.Normalized()

	step := workflow.NewFunctionNode("plan_step",
		func(ctx agent.Context, in planInput) (StepResult, error) {
			s, err := p.NextStep(ctx, in.History)
			if err != nil {
				return StepResult{}, err
			}
			return StepResult{
				Iteration: in.Iteration, Thought: s.Thought, Calls: len(s.Calls),
				Final: s.Final, Answer: s.Answer, ToolCalls: s.Calls, Usage: s.Usage, Raw: s.Raw,
			}, nil
		}, workflow.NodeConfig{})

	act := workflow.NewFunctionNode("call_tools",
		func(ctx agent.Context, calls []react.ToolCall) ([]react.Observation, error) {
			return dispatch(ctx, reg, calls), nil
		}, workflow.NodeConfig{})

	loop := workflow.NewDynamicNode[string, string]("react_loop",
		func(ctx agent.Context, task string, emit func(*session.Event) error) (string, error) {
			history := react.Seed(task)
			started := time.Now()
			var spent react.Usage
			var guard loopGuard
			for i := 1; ; i++ {
				if err := checkBudget(ctx, b, i, spent, started); err != nil {
					return "", err
				}
				r, err := workflow.RunNode[StepResult](ctx, step, planInput{Iteration: i, History: history})
				if err != nil {
					return "", fmt.Errorf("ітерація %d: %w", i, err)
				}
				spent = spent.Add(r.Usage)
				if err := say(ctx, emit, fmt.Sprintf("[%d] thought: %s", i, r.Thought)); err != nil {
					return "", err
				}
				if r.Final {
					return r.Answer, say(ctx, emit, "answer: "+r.Answer)
				}
				if err := checkStep(&guard, i, r.ToolCalls); err != nil {
					return "", err
				}
				obs, err := workflow.RunNode[[]react.Observation](ctx, act, r.ToolCalls)
				if err != nil {
					return "", fmt.Errorf("ітерація %d: %w", i, err)
				}
				for _, o := range obs {
					if err := say(ctx, emit, "    obs "+o.String()); err != nil {
						return "", err
					}
				}
				history = history.Append(react.Step{Thought: r.Thought, Calls: r.ToolCalls, Raw: r.Raw}, obs)
			}
		}, workflow.NodeConfig{})

	return workflowagent.New(workflowagent.Config{
		Name:        "react_graph_agent",
		Description: "ReAct-цикл як динамічний вузол workflow-графа з лімітом ітерацій.",
		Edges:       workflow.Chain(workflow.Start, loop),
	})
}

// say emits a text event, so the console shows Thought → Observation lines
// while the loop runs, not only the final answer.
func say(ctx agent.Context, emit func(*session.Event) error, text string) error {
	ev := session.NewEvent(ctx, ctx.InvocationID())
	ev.Content = genai.NewContentFromText(text+"\n", genai.RoleModel)
	return emit(ev)
}
