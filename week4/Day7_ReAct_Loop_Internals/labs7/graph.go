// ВИ: той самий цикл у workflow-графі ADK. Крок — вузол (NewFunctionNode),
// цикл — динамічний вузол (NewDynamicNode), що викликає крок через RunNode.
// Еталон API: sources/github/adk-go/examples/workflow/dynamic/basic/main.go.
package main

import (
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/react"
)

// StepResult — те, що вузол-крок пише в event log.
type StepResult struct {
	Iteration int    `json:"iteration"`
	Thought   string `json:"thought"`
	Calls     int    `json:"calls"`
	Final     bool   `json:"final"`
}

// NewGraphAgent збирає агента Start → react_loop.
func NewGraphAgent(p react.Planner, reg react.Registry, b react.Budget) (agent.Agent, error) {
	b = b.Normalized()
	history := react.Seed("")

	step := workflow.NewFunctionNode("plan_step",
		func(ctx agent.Context, turn int) (StepResult, error) {
			s, err := p.NextStep(ctx, history)
			if err != nil {
				return StepResult{}, err
			}
			// TODO(студент): збережіть s, щоб цикл нижче міг виконати s.Calls.
			return StepResult{Iteration: turn, Thought: s.Thought, Calls: len(s.Calls), Final: s.Final}, nil
		}, workflow.NodeConfig{})

	loop := workflow.NewDynamicNode[string, string]("react_loop",
		func(ctx agent.Context, task string, _ func(*session.Event) error) (string, error) {
			history = react.Seed(task)
			for i := 1; ; i++ {
				// TODO(студент): ліміт — той самий текст помилки й той самий ErrMaxIterations, що в react.go.
				if i > b.MaxIterations {
					return "", fmt.Errorf("вичерпано ліміт %d ітерацій: %w", b.MaxIterations, ErrMaxIterations)
				}
				r, err := workflow.RunNode[StepResult](ctx, step, i)
				if err != nil {
					return "", fmt.Errorf("ітерація %d: %w", i, err)
				}
				if r.Final {
					// TODO(студент): поверніть справжню відповідь кроку, а не думку.
					return r.Thought, nil
				}
				// TODO(студент): виконайте виклики кроку (reg.Invoke) і дайте
				// history = history.Append(step, observations).
				_ = reg
			}
		}, workflow.NodeConfig{})

	return workflowagent.New(workflowagent.Config{
		Name:        "react_graph_agent",
		Description: "ReAct-цикл як динамічний вузол workflow-графа з лімітом ітерацій.",
		Edges:       workflow.Chain(workflow.Start, loop),
	})
}
