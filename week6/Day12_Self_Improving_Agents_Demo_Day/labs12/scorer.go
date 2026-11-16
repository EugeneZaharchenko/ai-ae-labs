package main

// Measuring a strategy: every eval question runs through the FULL pipeline
// (plan → search → draft → critics → gate in eval mode → final report), and
// the pure metric counts the expected facts in the final report. The model
// writes; only deterministic code decides what it is worth — no LLM judge to
// talk into a better score.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/metric"
)

// EvalCaseTimeout is the hard stop of the eval node (brake (b)).
const EvalCaseTimeout = 60 * time.Second

// CaseResult is one eval question's outcome.
type CaseResult struct {
	ID, Report    string
	Found, Total  int
	Sources       []string
	Cited         []string
	CriticsPassed bool
	Rounds        int
	Tokens        int
	TimedOut      bool
	Failure       error
}

// PipelineScorer scores a strategy on an eval set.
type PipelineScorer struct {
	Lib      *Library
	Writer   model.LLM
	Critic   model.LLM
	Cases    []metric.Case
	Parallel int
	Timeout  time.Duration // per case; 0 = EvalCaseTimeout
}

// Score implements Scorer.
func (s *PipelineScorer) Score(ctx context.Context, st Strategy) (Measurement, error) {
	results := s.Run(ctx, st)
	m := Measurement{Detail: Describe(results)}
	found, total := 0, 0
	var errs []error
	for _, r := range results {
		m.Tokens += r.Tokens
		found += r.Found
		total += r.Total
		if r.Failure != nil {
			errs = append(errs, fmt.Errorf("кейс %s: %w", r.ID, r.Failure))
		}
	}
	if err := errors.Join(errs...); err != nil {
		// A network failure must not look like "the strategy got worse".
		return m, err
	}
	m.Score = metric.Ratio(found, total)
	return m, nil
}

// Run evaluates every case (at most Parallel at once), in case order.
func (s *PipelineScorer) Run(ctx context.Context, st Strategy) []CaseResult {
	out := make([]CaseResult, len(s.Cases))
	sem := make(chan struct{}, max(s.Parallel, 1))
	var wg sync.WaitGroup
	for i, c := range s.Cases {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = s.runCase(ctx, st, c)
		}()
	}
	wg.Wait()
	return out
}

// runCase runs one question inside the eval_case node, whose NodeConfig
// timeout is the per-question budget.
func (s *PipelineScorer) runCase(ctx context.Context, st Strategy, c metric.Case) CaseResult {
	res := CaseResult{ID: c.ID, Total: len(c.Facts)}
	meter := NewMeter()
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = EvalCaseTimeout
	}
	node := workflow.NewFunctionNode("eval_case", func(nctx agent.Context, question string) (string, error) {
		pipe, err := NewPipeline(PipelineConfig{
			Strategy: st, Lib: s.Lib, Writer: s.Writer, Critic: s.Critic,
			Gate: &Gate{Mode: GateAuto, Ledger: NewLedger(nil)}, Meter: meter,
		})
		if err != nil {
			return "", err
		}
		run, err := labrun.Run(nctx, pipe, question)
		if err != nil {
			return "", err
		}
		fillFromEvents(&res, run)
		return run.Final, nil
	}, workflow.NodeConfig{Timeout: timeout})
	ev, err := workflowagent.New(workflowagent.Config{
		Name: "eval_" + c.ID, Description: "One eval question", Edges: workflow.Chain(workflow.Start, node),
	})
	if err != nil {
		res.Failure = err
		return res
	}
	run, err := labrun.Run(ctx, ev, c.Question)
	res.Tokens = meter.Total()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "deadline exceeded") {
			res.TimedOut = true // over budget = no answer = 0 facts, not a broken measurement
			return res
		}
		res.Failure = err
		return res
	}
	res.Report = run.Final
	res.Found, _ = metric.Score(run.Final, c.Facts)
	return res
}

// fillFromEvents reads the pipeline's own trace back for the audit detail.
func fillFromEvents(res *CaseResult, run labrun.Result) {
	for _, e := range run.Events {
		if line, ok := e.Actions.StateDelta["w6p2:collect"].(string); ok {
			if _, after, ok := strings.Cut(line, " з "); ok {
				res.Sources = strings.Split(after, ", ")
			}
		}
		if line, ok := e.Actions.StateDelta["w6p2:approval_gate"].(string); ok {
			res.CriticsPassed = strings.Contains(line, "критики: пройшли")
		}
		for k := range e.Actions.StateDelta {
			if strings.HasPrefix(k, "w6p2:critic_loop#") {
				res.Rounds++
			}
		}
	}
	res.Cited = citedIDs(run.Final)
}

// Describe is the behaviour summary the proposer and the audit see: sources,
// critics, citations and the start of the report — never the expected facts.
func Describe(results []CaseResult) string {
	var b strings.Builder
	for _, r := range results {
		status := fmt.Sprintf("%d/%d facts", r.Found, r.Total)
		switch {
		case r.TimedOut:
			status += "; TIMED OUT (>60s)"
		case r.Failure != nil:
			status += "; ERROR " + clip(r.Failure.Error(), 80)
		}
		fmt.Fprintf(&b, "- %s: %s; evidence from [%s]; cited %v; critic rounds %d, passed %t; report: %q\n",
			r.ID, status, strings.Join(r.Sources, ", "), r.Cited, r.Rounds, r.CriticsPassed, clip(r.Report, 160))
	}
	return strings.TrimRight(b.String(), "\n")
}

// NoGoldenLeak refuses a strategy that contains expected facts of any eval set.
func NoGoldenLeak(cases []metric.Case) Guard {
	return func(s Strategy) error {
		if leaks := metric.Leaks(s.Raw, cases); len(leaks) > 0 {
			return fmt.Errorf("стратегія містить відповіді з eval-набору (%s): це шпаргалка, а не метод",
				strings.Join(leaks, "; "))
		}
		return nil
	}
}
