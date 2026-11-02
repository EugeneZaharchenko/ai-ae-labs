package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/googleadk"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/agentdb"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory/backend"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/ledger"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/llm"
)

const usage = `usage:
  go run . worker                                   # Temporal worker (LABS8_MODEL=demo for the offline model)
  go run . start [-session s1] [-user taras] MESSAGE  # one turn of the agent
  go run . counter                                  # llm_calls / refund_cases from .data/agent.db

env: LABS8_MODEL=demo, LEDGER_FAULTS=503,timeout,429, LEDGER_LATENCY=15s,
     AGENT_DB=.data/agent.db, TEMPORAL_ADDRESS=localhost:7233, MEMORY_BACKEND=dgraph, DGRAPH_ADDR=localhost:9080`

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "worker":
		return runWorker(ctx, out)
	case "start":
		return runStart(ctx, args[1:], out)
	case "counter":
		return runCounter(ctx, out)
	}
	return fmt.Errorf("unknown command %q\n%s", args[0], usage)
}

func dbPath() string {
	if p := os.Getenv("AGENT_DB"); p != "" {
		return p
	}
	return agentdb.DefaultPath
}

// dialTemporal is replaced in tests: there is no Temporal server there.
var dialTemporal = dial

func dial() (client.Client, error) {
	addr := os.Getenv("TEMPORAL_ADDRESS")
	if addr == "" {
		addr = client.DefaultHostPort
	}
	c, err := client.Dial(client.Options{HostPort: addr})
	if err != nil {
		return nil, fmt.Errorf("temporal at %s (is `temporal server start-dev --db-filename .data/temporal.db` running?): %w", addr, err)
	}
	return c, nil
}

// parseFaults reads LEDGER_FAULTS: a queue like "503,503,timeout".
func parseFaults(s string) (*ledger.FaultPlan, error) {
	plan := &ledger.FaultPlan{}
	for _, f := range strings.Split(s, ",") {
		switch strings.TrimSpace(f) {
		case "":
		case "503":
			plan.Push(ledger.Unavailable)
		case "timeout":
			plan.Push(ledger.Timeout)
		case "429":
			plan.Push(ledger.RateLimited)
		default:
			return nil, fmt.Errorf("LEDGER_FAULTS: unknown fault %q (want 503, timeout, 429)", f)
		}
	}
	return plan, nil
}

func parseLatency(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("LEDGER_LATENCY: %w", err)
	}
	return d, nil
}

// pickModel: LABS8_MODEL=demo is the offline rule model (Sloppy repeats the
// 03:00 incident); otherwise the course provider from apps/.env.
func pickModel(ctx context.Context) (model.LLM, string, error) {
	if os.Getenv("LABS8_MODEL") == "demo" {
		return llm.Demo{Sloppy: true}, "demo-rules (offline, LABS8_MODEL=demo)", nil
	}
	if err := modelcfg.LoadEnv("."); err != nil {
		return nil, "", err
	}
	m, choice, err := modelcfg.Load(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("no LLM provider: %w\nset a key in apps/.env or run offline: LABS8_MODEL=demo go run . worker", err)
	}
	return m, "live (" + choice.Reason + ")", nil
}

// newDesk builds the refund desk from the environment.
func newDesk(cases ledger.Cases) (*ledger.Desk, error) {
	faults, err := parseFaults(os.Getenv("LEDGER_FAULTS"))
	if err != nil {
		return nil, err
	}
	latency, err := parseLatency(os.Getenv("LEDGER_LATENCY"))
	if err != nil {
		return nil, err
	}
	return &ledger.Desk{Cases: cases, Faults: faults, Latency: latency}, nil
}

// adkConfig maps the workflow's logical model name to the real model. Counting
// writes every real call to .data/agent.db — outside the worker process, so the
// counter survives the kill -9 it is supposed to judge.
func adkConfig(m model.LLM, rec llm.Recorder) googleadk.Config {
	return googleadk.Config{Models: map[string]googleadk.ModelFactory{
		modelName: func(context.Context, string) (model.LLM, error) {
			return llm.Counting{Inner: m, Recorder: rec}, nil
		},
	}}
}

func runWorker(ctx context.Context, out io.Writer) error {
	m, which, err := pickModel(ctx)
	if err != nil {
		return err
	}
	db, err := agentdb.Open(dbPath())
	if err != nil {
		return err
	}
	defer db.Close()
	desk, err := newDesk(db.Cases())
	if err != nil {
		return err
	}
	memCfg := backend.ConfigFromEnv(os.Getenv)
	mem, closer, err := backend.Open(ctx, memCfg)
	if err != nil {
		return err
	}
	defer closer.Close()
	c, err := dial()
	if err != nil {
		return err
	}
	defer c.Close()

	plugin, err := googleadk.NewPlugin(adkConfig(m, db))
	if err != nil {
		return err
	}
	w := worker.New(c, taskQueue, worker.Options{Plugins: []worker.Plugin{plugin}})
	w.RegisterWorkflow(AgentWorkflow)
	w.RegisterActivity(&Activities{Desk: desk, Sessions: db.Sessions(), Memory: mem})
	fmt.Fprintf(out, "worker: task queue %s, model %s, memory %s at %s, faults %q, latency %s\n",
		taskQueue, which, memCfg.Backend, memCfg.DgraphAddr, os.Getenv("LEDGER_FAULTS"), desk.Latency)
	return w.Run(worker.InterruptCh())
}

func runStart(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	sessionID := fs.String("session", "s1", "session id: the same id continues the conversation")
	userID := fs.String("user", "taras", "user id: memory is scoped per user")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New(usage)
	}
	c, err := dialTemporal()
	if err != nil {
		return err
	}
	defer c.Close()

	req := Request{UserID: *userID, SessionID: *sessionID, Message: strings.Join(fs.Args(), " ")}
	id := fmt.Sprintf("turn-%s-%d", req.SessionID, time.Now().UnixMilli())
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: taskQueue}, AgentWorkflow, req)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "workflow %s (session %s)\n", run.GetID(), req.SessionID)
	var res Result
	err = run.Get(ctx, &res)
	printResult(out, res, err)
	return err
}

func printResult(out io.Writer, res Result, err error) {
	if res.LoadedEvents > 0 {
		fmt.Fprintf(out, "[ev:session] continued: %d events from earlier turns\n", res.LoadedEvents)
	}
	for _, line := range res.Events {
		fmt.Fprintln(out, line)
	}
	if err != nil {
		var appErr *temporal.ApplicationError
		if errors.As(err, &appErr) {
			fmt.Fprintf(out, "[ev:failed] %s: %s\n", appErr.Type(), appErr.Message())
			return
		}
		fmt.Fprintf(out, "[ev:failed] %v\n", err)
		return
	}
	fmt.Fprintf(out, "[ev:success] self_corrections=%d\n", res.SelfCorrections)
}

func runCounter(ctx context.Context, out io.Writer) error {
	db, err := agentdb.Open(dbPath())
	if err != nil {
		return err
	}
	defer db.Close()
	calls, err := db.CountLLMCalls(ctx)
	if err != nil {
		return err
	}
	cases, err := db.CountCases(ctx)
	if err != nil {
		return err
	}
	in, outTok, err := db.SumLLMTokens(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "llm_calls=%d refund_cases=%d tokens_in=%d tokens_out=%d\n", calls, cases, in, outTok)
	return nil
}
