package main

import (
	"errors"
	"time"

	"go.temporal.io/sdk/contrib/googleadk"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/ledger"
)

// toolRetry is the L1 policy for transient failures: the engine repeats, the
// model never hears about it. Semantic and fatal errors are not repeated —
// the same wrong arguments would fail the same way.
var toolRetry = &temporal.RetryPolicy{
	InitialInterval:        500 * time.Millisecond,
	BackoffCoefficient:     2,
	MaximumInterval:        5 * time.Second,
	MaximumAttempts:        4,
	NonRetryableErrorTypes: []string{errTypeSemantic, errTypeFatal},
}

// sideEffectOptions: StartToCloseTimeout is above LEDGER_LATENCY=15s of the
// kill -9 demo, otherwise every slow call would end as a timeout.
var sideEffectOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 30 * time.Second,
	RetryPolicy:         toolRetry,
}

var readOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 10 * time.Second,
	RetryPolicy:         toolRetry,
}

var errNoWorkflow = errors.New("tool called outside of a workflow (use googleadk.NewContext)")

// callActivity runs one tool call as an Activity and turns its failure into an
// Observation or a stop, using the per-turn budget.
func callActivity(ctx agent.Context, b *budget, name string, opts workflow.ActivityOptions, act, in any) (map[string]any, error) {
	wctx, ok := googleadk.WorkflowContext(ctx)
	if !ok {
		return nil, errNoWorkflow
	}
	opts.Summary = name
	var out map[string]any
	err := workflow.ExecuteActivity(workflow.WithActivityOptions(wctx, opts), act, in).Get(wctx, &out)
	if err == nil {
		return out, nil
	}
	obs, stop := b.observe(name, err)
	if stop != nil {
		return nil, stop
	}
	return obs.asMap(), nil
}

type searchArgs struct {
	Query string `json:"query" jsonschema:"Words to look for, e.g. A-114 refund"`
}

type openArgs struct {
	Names []string `json:"names" jsonschema:"Exact ids, e.g. [\"A-114\"]"`
}

// newTools builds the agent's tools for one turn. Every tool runs an Activity;
// only the call to the Activity happens in the workflow.
func newTools(b *budget) ([]tool.Tool, error) {
	var a *Activities // method values give the registered Activity names
	specs := []struct {
		cfg functiontool.Config
		new func(functiontool.Config) (tool.Tool, error)
	}{
		{functiontool.Config{
			Name:        "open_refund_case",
			Description: "Opens a refund case for one transaction of one merchant. Needs transaction_id and merchant_id exactly as the user wrote them. May return status invalid_argument with an observation: read it, fix the named field, call again.",
		}, func(c functiontool.Config) (tool.Tool, error) {
			return functiontool.New(c, func(ctx agent.Context, in ledger.RefundCaseInput) (map[string]any, error) {
				return callActivity(ctx, b, c.Name, sideEffectOptions, a.OpenRefundCase, in)
			})
		}},
		{functiontool.Config{
			Name:        "get_merchant_payouts",
			Description: "Read-only summary of a merchant's payouts.",
		}, func(c functiontool.Config) (tool.Tool, error) {
			return functiontool.New(c, func(ctx agent.Context, in ledger.PayoutsInput) (map[string]any, error) {
				return callActivity(ctx, b, c.Name, readOptions, a.GetMerchantPayouts, in)
			})
		}},
		{functiontool.Config{
			Name:        "search_nodes",
			Description: "Search long-term memory of earlier conversations by words.",
		}, func(c functiontool.Config) (tool.Tool, error) {
			return functiontool.New(c, func(ctx agent.Context, in searchArgs) (map[string]any, error) {
				return callActivity(ctx, b, c.Name, readOptions, a.SearchNodes,
					SearchNodesInput{AppName: ctx.AppName(), UserID: ctx.UserID(), Query: in.Query})
			})
		}},
		{functiontool.Config{
			Name:        "open_nodes",
			Description: "Everything remembered about known ids such as merchant A-114 or txn-2026-07-118845.",
		}, func(c functiontool.Config) (tool.Tool, error) {
			return functiontool.New(c, func(ctx agent.Context, in openArgs) (map[string]any, error) {
				return callActivity(ctx, b, c.Name, readOptions, a.OpenNodes,
					OpenNodesInput{AppName: ctx.AppName(), UserID: ctx.UserID(), Names: in.Names})
			})
		}},
	}
	tools := make([]tool.Tool, 0, len(specs))
	for _, s := range specs {
		t, err := s.new(s.cfg)
		if err != nil {
			return nil, err
		}
		tools = append(tools, t)
	}
	return tools, nil
}
