// Package refund implements the shared, model-free Week 2 graph.
package refund

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/workflow"
)

const (
	AppName            = "first_graph_agent"
	StateKeyCaseID     = "refund:last_case_id"
	StateKeyMerchantID = "refund:last_merchant_id"
	StateKeyStatus     = "refund:last_status"
)

type Input struct {
	TransactionID string `json:"transaction_id" jsonschema:"transaction identifier, e.g. txn-2026-07-118845"`
	MerchantID    string `json:"merchant_id" jsonschema:"merchant identifier, e.g. A-114"`
}

type Output struct {
	CaseID        string `json:"case_id"`
	TransactionID string `json:"transaction_id"`
	MerchantID    string `json:"merchant_id"`
	Status        string `json:"status" jsonschema:"pending or already_open"`
}

var (
	transactionID = regexp.MustCompile(`^txn(-[a-z0-9]+)+$`)
	merchantID    = regexp.MustCompile(`^[a-z]+-[0-9]+$`)
)

// Registry is an in-memory case register, not a payment processor.
// ponytail: one process-wide register; use durable, tenant-scoped storage before production.
type Registry struct {
	mu    sync.Mutex
	cases map[string]Output
}

func (r *Registry) OpenCase(ctx agent.Context, in Input) (Output, error) {
	txn := strings.ToLower(strings.TrimSpace(in.TransactionID))
	merchant := strings.ToUpper(strings.TrimSpace(in.MerchantID))
	if !transactionID.MatchString(txn) {
		return Output{}, fmt.Errorf("invalid transaction id: %q", in.TransactionID)
	}
	if merchant != "A-114" && merchant != "B-207" {
		return Output{}, fmt.Errorf("unknown merchant id: %q; available: A-114, B-207", in.MerchantID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cases == nil {
		r.cases = make(map[string]Output)
	}
	id := "rc-" + txn + "-" + merchant
	out, exists := r.cases[id]
	if exists {
		out.Status = "already_open"
	} else {
		out = Output{id, txn, merchant, "pending"}
		r.cases[id] = out
	}
	// These keys live across turns in this session, not across process restarts.
	actions := ctx.Actions()
	if actions.StateDelta == nil {
		actions.StateDelta = make(map[string]any)
	}
	actions.StateDelta[StateKeyCaseID] = out.CaseID
	actions.StateDelta[StateKeyMerchantID] = merchant
	actions.StateDelta[StateKeyStatus] = out.Status
	return out, nil
}

func Prepare(_ agent.Context, msg string) (Input, error) {
	var in Input
	for _, token := range strings.FieldsFunc(strings.ToLower(msg), func(r rune) bool {
		return r != '-' && r != '_' && !('a' <= r && r <= 'z') && !('0' <= r && r <= '9')
	}) {
		if in.TransactionID == "" && transactionID.MatchString(token) {
			in.TransactionID = token
			continue
		}
		if in.MerchantID == "" && merchantID.MatchString(token) {
			in.MerchantID = strings.ToUpper(token)
		}
	}
	if in.TransactionID == "" || in.MerchantID == "" {
		return Input{}, fmt.Errorf("очікую ID транзакції та мерчанта, отримано: %q", msg)
	}
	return in, nil
}

func Format(_ agent.Context, out Output) (string, error) {
	if out.CaseID == "" || out.TransactionID == "" || out.MerchantID == "" ||
		(out.Status != "pending" && out.Status != "already_open") {
		return "", fmt.Errorf("incomplete or invalid refund result: %+v", out)
	}
	return fmt.Sprintf("Кейс %s: транзакція %s, мерчант %s, статус %s",
		out.CaseID, out.TransactionID, out.MerchantID, out.Status), nil
}

func NewTool(reg *Registry) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "open_refund_case",
		Description: "Opens a refund case for an existing LEDGERWORKS merchant transaction. Does not transfer money.",
	}, reg.OpenCase)
}

func NewGraph(reg *Registry) (agent.Agent, error) {
	refundTool, err := NewTool(reg)
	if err != nil {
		return nil, fmt.Errorf("create refund tool: %w", err)
	}
	// No retries: all work is local; validation errors cannot improve on retry.
	cfg := workflow.NodeConfig{}
	prepare := workflow.NewFunctionNode("prepare", Prepare, cfg)
	openCase, err := workflow.NewToolNodeTyped[Input, Output](refundTool, cfg)
	if err != nil {
		return nil, fmt.Errorf("create refund node: %w", err)
	}
	format := workflow.NewFunctionNode("format", Format, cfg)
	return workflowagent.New(workflowagent.Config{
		Name:        AppName,
		Description: "LEDGERWORKS: prepare a refund request, open its case, format the result.",
		Edges:       workflow.Chain(workflow.Start, prepare, openCase, format),
	})
}
