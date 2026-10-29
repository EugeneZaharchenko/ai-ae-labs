// Manual ReAct loop. No ADK imports here: it is just a `for` and some `if`s
// over the Planner interface and the tool registry.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/react"
)

var (
	// ErrEmptyStep: the planner returned a step that is neither final nor has calls.
	// Without this check the loop would spin to the limit doing nothing.
	ErrEmptyStep = errors.New("step has no calls and no final answer")
	// ErrTokenBudget: Budget.MaxTokens is spent.
	ErrTokenBudget = errors.New("token budget exhausted")
)

// loopRepeats is how many identical steps in a row count as a loop.
const loopRepeats = 3

// Run executes Thought → Action → Observation until Step.Final is true or the
// Budget is spent. The trace is returned on error too.
func Run(ctx context.Context, p react.Planner, reg react.Registry, task string, b react.Budget) (string, react.Trace, error) {
	b = b.Normalized()
	started := time.Now()
	if b.MaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.MaxDuration)
		defer cancel()
	}
	history := react.Seed(task)
	var tr react.Trace
	var guard loopGuard
	for i := 1; ; i++ {
		if err := checkBudget(ctx, b, i, tr.Usage, started); err != nil {
			return "", tr, err
		}
		step, err := p.NextStep(ctx, history)
		if err != nil {
			return "", tr, fmt.Errorf("ітерація %d: %w", i, err)
		}
		tr.Iterations = i
		tr.Steps = append(tr.Steps, step)
		tr.Usage = tr.Usage.Add(step.Usage)
		if step.Final {
			return step.Answer, tr, nil
		}
		if err := checkStep(&guard, i, step.Calls); err != nil {
			return "", tr, err
		}
		obs := dispatch(ctx, reg, step.Calls)
		tr.Observations = append(tr.Observations, obs)
		history = history.Append(step, obs)
	}
}

// checkBudget is called before every planner call. Both versions use it, so
// the limit and its error text are the same in the manual loop and the graph.
// The time check is needed for the graph: there the context has no deadline.
func checkBudget(ctx context.Context, b react.Budget, i int, spent react.Usage, started time.Time) error {
	if i > b.MaxIterations {
		return fmt.Errorf("вичерпано ліміт %d ітерацій: %w", b.MaxIterations, ErrMaxIterations)
	}
	if b.MaxTokens > 0 && spent.Total() >= b.MaxTokens {
		return fmt.Errorf("вичерпано бюджет %d токенів (витрачено %d): %w", b.MaxTokens, spent.Total(), ErrTokenBudget)
	}
	if b.MaxDuration > 0 && time.Since(started) > b.MaxDuration {
		return fmt.Errorf("вичерпано час %s: %w", b.MaxDuration, context.DeadlineExceeded)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("ітерація %d: %w", i, err)
	}
	return nil
}

// checkStep rejects a non-final step without calls and a step that repeats
// the previous ones (checked before dispatch, so the repeated call never runs).
func checkStep(g *loopGuard, i int, calls []react.ToolCall) error {
	if len(calls) == 0 {
		return fmt.Errorf("ітерація %d: %w", i, ErrEmptyStep)
	}
	if g.repeated(calls) {
		return fmt.Errorf("ітерація %d: однаковий виклик %d рази поспіль: %w", i, loopRepeats, ErrLoopDetected)
	}
	return nil
}

// loopGuard counts how many steps in a row asked for the same calls with the
// same args. CallIDs are not part of the key: a planner gives every repeat a
// new ID, but it is still the same action.
type loopGuard struct {
	last  string
	count int
}

func (g *loopGuard) repeated(calls []react.ToolCall) bool {
	key := callsKey(calls)
	if key == g.last {
		g.count++
	} else {
		g.last, g.count = key, 1
	}
	return g.count >= loopRepeats
}

func callsKey(calls []react.ToolCall) string {
	type action struct {
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
	}
	acts := make([]action, len(calls))
	for i, c := range calls {
		acts[i] = action{c.Name, c.Args}
	}
	// json.Marshal sorts map keys, so equal args give equal keys.
	key, err := json.Marshal(acts)
	if err != nil {
		return fmt.Sprint(acts)
	}
	return string(key)
}

// dispatch runs the calls of one step and returns one Observation per call,
// in the order the planner asked for them.
//
// Read-only calls do not depend on each other, so they run in parallel.
// Side-effecting calls (Tool.SideEffecting) run one by one in the asked order,
// after the reads: a ticket must not be created twice or in a random order.
func dispatch(ctx context.Context, reg react.Registry, calls []react.ToolCall) []react.Observation {
	obs := make([]react.Observation, len(calls))
	var wg sync.WaitGroup
	for i, c := range calls {
		if reg[c.Name].SideEffecting {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			obs[i] = reg.Invoke(ctx, c)
		}()
	}
	wg.Wait()
	for i, c := range calls {
		if reg[c.Name].SideEffecting {
			obs[i] = reg.Invoke(ctx, c)
		}
	}
	return obs
}
