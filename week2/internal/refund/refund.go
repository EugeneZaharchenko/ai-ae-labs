// Package refund implements the shared Week 2 refund domain: the typed
// contract, the in-memory case register and the `open_refund_case` tool.
//
// The workflow graph that wires these steps together is composed per lab
// (labs3/agent_graph.go, labs4/agent_graph.go), so each lab owns its topology.
package refund

import (
	"context"
	"fmt"
	"iter"
	"regexp"
	"strings"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const (
	AppName            = "first_graph_agent"
	StateKeyCaseID     = "refund:last_case_id"
	StateKeyMerchantID = "refund:last_merchant_id"
	StateKeyStatus     = "refund:last_status"
)

// Route names returned by Classify. They are the domain's vocabulary for
// "which request is this"; the graph turns them into edges, which is the part
// each lab owns.
const (
	RouteRefund      = "refund"
	RouteStatus      = "status"
	RouteOutOfDomain = "out_of_domain"
)

type Input struct {
	TransactionID string `json:"transaction_id" jsonschema:"transaction identifier, e.g. txn-2026-07-118845"`
	MerchantID    string `json:"merchant_id" jsonschema:"merchant identifier, e.g. A-114"`
}

// StatusInput asks about a case that was opened earlier. It is deliberately
// narrower than Input: reading a case needs the case ID the register issued,
// not the transaction it came from.
type StatusInput struct {
	CaseID string `json:"case_id" jsonschema:"refund case identifier, e.g. rc-txn-2026-07-118845-A-114"`
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
	// caseID is the shape OpenCase mints: "rc-" + transaction + "-" + merchant.
	// Matching it is what lets a status request name a case without repeating
	// the merchant ID that is already inside the ID.
	caseID = regexp.MustCompile(`^rc(-[a-z0-9]+)+$`)
)

// Registry is an in-memory case register, not a payment processor.
// ponytail: one process-wide register; use durable, tenant-scoped storage before production.
type Registry struct {
	mu sync.Mutex
	// cases is keyed by the canonical (lowercased) case ID, so a caller may
	// ask about a case in any casing. The entry keeps the minted ID, which is
	// what the transcript reports back.
	cases map[string]Output
	// llm, when set, is the alternative classifier: the router asks a model
	// for the route name instead of calling Classify. Optional on purpose —
	// nil is the keyless, network-free default every test relies on.
	llm Classifier
}

// Classifier is the model half of routing: the one method a classifier needs to
// be a model.LLM. It is declared here rather than imported from the ADK so the
// domain package never depends on a provider SDK, and so a lab can pass a
// scripted fake in a test.
type Classifier interface {
	Name() string
	GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error]
}

// ClassifierModel returns the model the register routes with, or nil for the
// deterministic default.
func (r *Registry) ClassifierModel() Classifier {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.llm
}

// SetClassifier installs a model as the graph's classifier source. It is
// separate from Classify because routing is the lab's choice, not the domain's:
// the register stays the same whether a model or a pure function picked the
// branch.
func (r *Registry) SetClassifier(c Classifier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.llm = c
}

// canonicalCaseID is the register's key form. Case IDs are compared
// case-insensitively because they arrive from humans and models, which do not
// agree on casing.
func canonicalCaseID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
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
	out, exists := r.cases[canonicalCaseID(id)]
	if exists {
		out.Status = "already_open"
	} else {
		out = Output{id, txn, merchant, "pending"}
		r.cases[canonicalCaseID(id)] = out
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

// Lookup reads a case that is already in the register. It never invents a
// status: an ID that was not opened in this process is an error, so a status
// answer cannot be confused with an opened case.
func (r *Registry) Lookup(ctx agent.Context, in StatusInput) (Output, error) {
	id := canonicalCaseID(in.CaseID)
	if !caseID.MatchString(id) {
		return Output{}, fmt.Errorf("invalid case id: %q", in.CaseID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out, exists := r.cases[id]
	if !exists {
		return Output{}, fmt.Errorf("unknown case id: %q", in.CaseID)
	}
	// Reading is a business event too: it leaves the same audit keys behind,
	// so the log shows what the answer was based on.
	actions := ctx.Actions()
	if actions.StateDelta == nil {
		actions.StateDelta = make(map[string]any)
	}
	actions.StateDelta[StateKeyCaseID] = out.CaseID
	actions.StateDelta[StateKeyMerchantID] = out.MerchantID
	actions.StateDelta[StateKeyStatus] = out.Status
	return out, nil
}

func Prepare(_ agent.Context, msg string) (Input, error) {
	var in Input
	for _, token := range tokens(msg) {
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

// PrepareStatus extracts a case ID from a status request. It is the read-side
// mirror of Prepare and needs one ID, not two.
func PrepareStatus(_ agent.Context, msg string) (StatusInput, error) {
	var in StatusInput
	for _, token := range tokens(msg) {
		if caseID.MatchString(token) {
			in.CaseID = token
			break
		}
	}
	if in.CaseID == "" {
		return StatusInput{}, fmt.Errorf("очікую ID кейса, отримано: %q", msg)
	}
	return in, nil
}

// Classify names the request so the graph can route on it. It is a pure
// function of the message: the same wording always takes the same branch,
// which is what makes the routing testable without a model.
//
// IDs outrank vocabulary, in this order:
//
//  1. A case ID is a status request even without the word "статус" — asking
//     about a case cannot mean anything else.
//  2. A transaction ID plus a merchant ID is a refund request, whatever else
//     the message says. Routing an unopenable request to the refund branch is
//     deliberate: there it fails on the merchant check and reports why, which
//     is a better answer than "out of domain" for a request that plainly is
//     not.
//  3. Only then does vocabulary decide, status before refund, and anything
//     left over is out of domain.
func Classify(_ agent.Context, msg string) (string, error) {
	var hasTxn, hasMerchant bool
	for _, token := range tokens(msg) {
		switch {
		case caseID.MatchString(token):
			return RouteStatus, nil
		case transactionID.MatchString(token):
			hasTxn = true
		case merchantID.MatchString(token):
			hasMerchant = true
		}
	}
	if hasTxn && hasMerchant {
		return RouteRefund, nil
	}
	lowered := strings.ToLower(msg)
	for _, word := range statusWords {
		if strings.Contains(lowered, word) {
			return RouteStatus, nil
		}
	}
	for _, word := range refundWords {
		if strings.Contains(lowered, word) {
			return RouteRefund, nil
		}
	}
	return RouteOutOfDomain, nil
}

// statusWords are checked before refundWords, and the order matters: a message
// that mentions both ("check the status of the refund ...") is a status
// question. The lists stay disjoint otherwise — a bare "check" is not a status
// word, or "check the refund for txn-123" would route to the wrong branch.
var (
	statusWords = []string{"status", "статус"}
	refundWords = []string{"refund", "return", "поверн"}
)

// tokens splits a message into lowercase candidate identifiers. Letters, digits
// and the separators the ID formats use survive; everything else is a break.
func tokens(msg string) []string {
	return strings.FieldsFunc(strings.ToLower(msg), func(r rune) bool {
		return r != '-' && r != '_' && !('a' <= r && r <= 'z') && !('0' <= r && r <= '9')
	})
}

func Format(_ agent.Context, out Output) (string, error) {
	if out.CaseID == "" || out.TransactionID == "" || out.MerchantID == "" ||
		(out.Status != "pending" && out.Status != "already_open") {
		return "", fmt.Errorf("incomplete or invalid refund result: %+v", out)
	}
	return fmt.Sprintf("Кейс %s: транзакція %s, мерчант %s, статус %s",
		out.CaseID, out.TransactionID, out.MerchantID, out.Status), nil
}

// FormatStatus renders a status answer. It states where the answer came from
// because "pending" read out of the register and "pending" invented by a model
// must not look the same in a transcript.
func FormatStatus(_ agent.Context, out Output) (string, error) {
	if out.CaseID == "" || out.TransactionID == "" || out.MerchantID == "" ||
		(out.Status != "pending" && out.Status != "already_open") {
		return "", fmt.Errorf("incomplete or invalid refund result: %+v", out)
	}
	return fmt.Sprintf("Кейс %s (транзакція %s, мерчант %s) уже в реєстрі, статус %s",
		out.CaseID, out.TransactionID, out.MerchantID, out.Status), nil
}

// OutOfDomain answers a request this domain does not handle. It exists so the
// routing graph has a real terminal step for every route instead of a branch
// that silently produces nothing.
func OutOfDomain(_ agent.Context, msg string) (string, error) {
	if strings.TrimSpace(msg) == "" {
		return "", fmt.Errorf("порожній запит")
	}
	return fmt.Sprintf("Я обробляю лише повернення LEDGERWORKS: відкриття кейса або його статус. Запит %q поза цим доменом.", msg), nil
}

func NewTool(reg *Registry) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "open_refund_case",
		Description: "Opens a refund case for an existing LEDGERWORKS merchant transaction. Does not transfer money.",
	}, reg.OpenCase)
}

// NewStatusTool exposes the read side of the register. Separate from NewTool
// because a model that can only open cases will, asked for a status, answer
// from its own head — the exact failure the graph exists to prevent.
func NewStatusTool(reg *Registry) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "check_refund_status",
		Description: "Reads the status of an existing LEDGERWORKS refund case by its case ID. Does not open or change anything.",
	}, reg.Lookup)
}
