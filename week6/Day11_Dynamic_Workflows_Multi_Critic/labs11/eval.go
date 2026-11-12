package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/billing"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/gate"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/usage"
)

// models — the maker and one model per LLM critic role.
type models struct {
	maker   model.LLM
	critics map[string]model.LLM
}

type runOptions struct {
	maxAttempts int
	parallel    bool
	progress    io.Writer
}

// buildAgent assembles the whole agent for one task.
func buildAgent(task billing.Task, m models, opt runOptions, meter *usage.Meter) (agent.Agent, error) {
	if opt.progress == nil {
		opt.progress = io.Discard
	}
	makerAgent, err := llmagent.New(llmagent.Config{
		Name:                "maker",
		Model:               m.maker,
		Description:         "Пише і виправляє код за завданням та зауваженнями.",
		Instruction:         makerInstruction,
		AfterModelCallbacks: []llmagent.AfterModelCallback{meter.Callback("maker")},
	})
	if err != nil {
		return nil, fmt.Errorf("maker: %w", err)
	}
	makerNode, err := workflow.NewAgentNodeTyped[Draft, string](makerAgent, workflow.NodeConfig{Timeout: makerTimeout})
	if err != nil {
		return nil, fmt.Errorf("вузол maker: %w", err)
	}
	panel, err := newCriticPanel(m.critics, meter, opt.parallel)
	if err != nil {
		return nil, err
	}
	loop := newLoop(loopConfig{
		task:        task,
		maxAttempts: opt.maxAttempts,
		maker:       makerNode,
		compiler:    newCompilerNode(gate.Runner{StageTimeout: goCallTimeout}, task.Visible),
		panel:       panel,
		meter:       meter,
		progress:    opt.progress,
	})
	return workflowagent.New(workflowagent.Config{
		Name:        "multi_critic_coding_agent",
		Description: "Динамічний цикл maker → compiler → критики → фідбек.",
		Edges:       workflow.Chain(workflow.Start, loop),
	})
}

// taskResult — one row of the eval table plus the trace for printing.
type taskResult struct {
	TaskID       string
	Accepted     bool
	Attempts     int
	CriticRounds int
	HoldoutOK    bool
	HoldoutStage string
	Elapsed      time.Duration
	Tokens       usage.Tokens
	Err          string
	Code         string
	Events       []*session.Event
	Meter        *usage.Meter
}

// FalseAccept — the loop said "done", but the hidden tests say the code is wrong.
func (r taskResult) FalseAccept() bool { return r.Accepted && !r.HoldoutOK }

// runTask runs the agent on one task and then checks the result on Holdout,
// which neither the maker nor the critics have seen.
func runTask(ctx context.Context, task billing.Task, m models, opt runOptions) taskResult {
	meter := &usage.Meter{}
	res := taskResult{TaskID: task.ID, Meter: meter}
	start := time.Now()

	a, err := buildAgent(task, m, opt, meter)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	out, runErr := labrun.Run(ctx, a, task.Prompt)
	res.Events = out.Events
	if runErr != nil {
		res.Err = runErr.Error()
	} else {
		res.Code = out.Final
		res.Accepted = out.Final != ""
	}
	res.Elapsed = time.Since(start)
	res.Attempts, res.CriticRounds = loopCounts(out.Events)
	res.Tokens = meter.Total()

	if res.Code != "" {
		rep := gate.Runner{StageTimeout: goCallTimeout}.Grade(ctx, gate.Spec{Code: res.Code, Test: task.Holdout})
		res.HoldoutOK, res.HoldoutStage = rep.OK, rep.Stage
	}
	return res
}

// loopCounts reads attempts and critic rounds from the final loop event.
func loopCounts(events []*session.Event) (attempts, rounds int) {
	for _, ev := range events {
		for _, key := range []string{keyDone, keyExhausted} {
			if s, ok := ev.Actions.StateDelta[key].(map[string]any); ok {
				return toInt(s["attempts"]), toInt(s["critic_rounds"])
			}
		}
	}
	// loop died with an error: count attempt events
	for _, ev := range events {
		for k := range ev.Actions.StateDelta {
			if strings.HasPrefix(k, "w6p1:attempt:") {
				attempts++
			}
		}
	}
	return attempts, 0
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

// eventLog prints every w6p1:* entry from the event log in order.
func eventLog(events []*session.Event) string {
	var b strings.Builder
	for _, ev := range events {
		keys := make([]string, 0, len(ev.Actions.StateDelta))
		for k := range ev.Actions.StateDelta {
			if strings.HasPrefix(k, "w6p1:") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s = %v\n", k, ev.Actions.StateDelta[k])
		}
	}
	return b.String()
}

// evalTable renders the markdown table for the README.
func evalTable(rs []taskResult) string {
	var b strings.Builder
	b.WriteString("| задача | цикл прийняв | holdout | false accept | спроб | раундів критиків | час, с | токени in/out |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	falseAccepts, total := 0, 0
	for _, r := range rs {
		if r.FalseAccept() {
			falseAccepts++
		}
		total += r.Tokens.Total()
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %d | %.1f | %d/%d |\n",
			r.TaskID, yesNo(r.Accepted), holdoutCell(r), yesNo(r.FalseAccept()),
			r.Attempts, r.CriticRounds, r.Elapsed.Seconds(), r.Tokens.Prompt, r.Tokens.Output)
	}
	fmt.Fprintf(&b, "\nfalse accepts: %d з %d; токенів усього: %d\n", falseAccepts, len(rs), total)
	return b.String()
}

func holdoutCell(r taskResult) string {
	switch {
	case r.HoldoutOK:
		return "так"
	case r.HoldoutStage != "":
		return "ні (" + r.HoldoutStage + ")"
	}
	return "—"
}

func yesNo(b bool) string {
	if b {
		return "так"
	}
	return "ні"
}
