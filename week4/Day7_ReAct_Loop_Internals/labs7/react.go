// ВИ: мануальний ReAct-цикл. Тут немає імпортів ADK — це `for` і `if` над
// інтерфейсом Planner та реєстром інструментів.
//
// Словник (ToolCall, Step, Planner, Budget, Trace, Registry, Observation) уже
// є в internal/react — прочитайте його перед тим, як писати цикл.
package main

import (
	"context"

	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/react"
)

// Run виконує цикл Thought → Action → Observation, доки Step.Final не стане
// true або не вичерпано Budget.
//
// TODO(студент), по порядку:
//  1. b = b.Normalized(); history := react.Seed(task).
//  2. for i := 1; ; i++ — якщо i > b.MaxIterations, поверніть
//     fmt.Errorf("вичерпано ліміт %d ітерацій: %w", b.MaxIterations, ErrMaxIterations).
//  3. step, err := p.NextStep(ctx, history); якщо step.Final — поверніть step.Answer.
//  4. Для кожного step.Calls викличте reg.Invoke(ctx, call) і зберіть спостереження
//     В ТОМУ Ж ПОРЯДКУ (кожне Observation несе CallID свого виклику).
//  5. history = history.Append(step, observations) — без цього наступний крок
//     не побачить результату.
//
// Trace повертайте і разом із помилкою: о 3-й ночі саме він показує, де застрягло.
func Run(ctx context.Context, p react.Planner, reg react.Registry, task string, b react.Budget) (string, react.Trace, error) {
	_, _, _, _, _ = ctx, p, reg, task, b
	return "", react.Trace{}, ErrNotImplemented
}
