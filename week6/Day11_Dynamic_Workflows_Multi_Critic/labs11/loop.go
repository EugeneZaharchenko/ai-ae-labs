package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/billing"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/usage"
)

// ErrBudgetExhausted — the loop used all attempts and nothing was accepted.
var ErrBudgetExhausted = errors.New("budget exhausted")

// Event log keys (session state, StateDelta).
const (
	keyAttempt   = "w6p1:attempt:%d"
	keyDone      = "w6p1:loop_done"
	keyExhausted = "w6p1:budget_exhausted"
	keyError     = "w6p1:loop_error"
)

type loopConfig struct {
	task        billing.Task
	maxAttempts int
	maker       workflow.Node
	compiler    workflow.Node
	panel       criticPanel
	meter       *usage.Meter
	progress    io.Writer
}

// newLoop is the dynamic node: maker -> compiler -> LLM critics -> feedback -> maker.
func newLoop(cfg loopConfig) workflow.Node {
	return workflow.NewDynamicNode("multi_critic_loop",
		func(ctx agent.Context, _ string, emit func(*session.Event) error) (string, error) {
			return runLoop(ctx, cfg, emit)
		},
		workflow.NodeConfig{Timeout: loopTimeout(cfg.maxAttempts)},
	)
}

func runLoop(ctx agent.Context, cfg loopConfig, emit func(*session.Event) error) (string, error) {
	d := Draft{Task: cfg.task.Prompt, Tests: cfg.task.Visible}
	criticRounds := 0

	for d.Attempt = 1; d.Attempt <= cfg.maxAttempts; d.Attempt++ {
		before := cfg.meter.Spent()

		out, err := workflow.RunNode[string](ctx, cfg.maker, d, workflow.WithUseSubBranch())
		if err != nil {
			err = fmt.Errorf("maker (спроба %d): %w", d.Attempt, err)
			_ = emit(stateEvent(ctx, keyError, map[string]any{"attempt": d.Attempt, "error": err.Error()}))
			return "", err
		}
		d.Code = stripFence(out)
		fmt.Fprintf(cfg.progress, "спроба %d: generate → %d рядків коду\n", d.Attempt, countLines(d.Code))

		// 1. Objective critic first: no reason to pay for LLM critics
		// if the code does not even compile.
		verdicts := []Verdict{runCompiler(ctx, cfg.compiler, d)}
		var rejected []string

		// 2. LLM critics only when build/vet/tests are green.
		if verdicts[0].Passed {
			criticRounds++
			raws, err := cfg.panel.run(ctx, d)
			if err != nil {
				err = fmt.Errorf("панель критиків (спроба %d): %w", d.Attempt, err)
				_ = emit(stateEvent(ctx, keyError, map[string]any{"attempt": d.Attempt, "error": err.Error()}))
				return "", err
			}
			for _, role := range llmRoles {
				v, perr := ParseVerdict(raws[role], role)
				if perr != nil {
					rejected = append(rejected, role+": "+perr.Error())
					v = rejectedVerdict(role, perr)
				}
				verdicts = append(verdicts, v)
			}
		}

		accept, feedback := decide(verdicts)
		for _, v := range verdicts {
			fmt.Fprintf(cfg.progress, "  вердикт %s\n", verdictLine(v))
		}
		decision := "retry"
		if accept {
			decision = "accept"
		}
		fmt.Fprintf(cfg.progress, "  гейт: %s\n", decision)

		if err := emit(stateEvent(ctx, fmt.Sprintf(keyAttempt, d.Attempt), map[string]any{
			"generate": fmt.Sprintf("%d рядків коду", countLines(d.Code)),
			"verdicts": verdictLines(verdicts),
			"rejected": rejected,
			"decision": decision,
			"tokens":   cfg.meter.Spent() - before,
		})); err != nil {
			return "", err
		}

		if accept {
			if err := emit(stateEvent(ctx, keyDone, map[string]any{
				"attempts": d.Attempt, "critic_rounds": criticRounds, "tokens": cfg.meter.Spent(),
			})); err != nil {
				return "", err
			}
			return d.Code, nil
		}
		d.Feedback = feedback
	}

	err := fmt.Errorf("%w: %d спроб без проходження всіх критиків", ErrBudgetExhausted, cfg.maxAttempts)
	_ = emit(stateEvent(ctx, keyExhausted, map[string]any{
		"attempts": cfg.maxAttempts, "critic_rounds": criticRounds, "tokens": cfg.meter.Spent(), "error": err.Error(),
	}))
	return "", err
}

// stateEvent writes one entry into the event log via StateDelta, so the whole
// trace can be read later from session state.
func stateEvent(ctx agent.Context, key string, value map[string]any) *session.Event {
	ev := session.NewEvent(ctx, ctx.InvocationID())
	ev.Actions.StateDelta[key] = value
	return ev
}

func verdictLine(v Verdict) string {
	if v.Passed {
		return v.Critic + ": passed"
	}
	return fmt.Sprintf("%s: FAILED — %s", v.Critic, short(strings.Join(v.Comments, " | "), 160))
}

func verdictLines(vs []Verdict) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, verdictLine(v))
	}
	return out
}

func countLines(s string) int {
	if strings.TrimSpace(s) == "" {
		return 0
	}
	return strings.Count(strings.TrimRight(s, "\n"), "\n") + 1
}

func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
