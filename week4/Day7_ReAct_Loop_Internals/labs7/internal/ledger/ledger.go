// Package ledger is the LEDGERWORKS domain of the Day 7 lab: three investigation
// tools over deterministic fixtures. Day 7 keeps its own copy (no package above
// Day level), separate from the Day 8 lab's.
//
// The tools are shaped by what breaks real agent loops, not by what is
// convenient in a demo:
//
//   - the payout ledger returns a PAGE, not everything — an unbounded tool
//     result is the quickest way to burn a token budget in one step;
//   - an unknown merchant is an error the model can read ("merchant ... not
//     found"), the failure that Day 8 builds on;
//   - ticket creation is idempotent on the title, because a loop that retries
//     or repeats a call must not open a second ticket.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/react"
)

// ErrMerchantNotFound: no such merchant in the ledger.
var ErrMerchantNotFound = errors.New("merchant not found")

// DefaultPayoutRows is the page size when the model does not ask for one.
const DefaultPayoutRows = 3

// Ledger is the fixture "infrastructure". Safe for concurrent use: the
// dispatcher may run read-only calls in parallel.
type Ledger struct {
	mu      sync.Mutex
	tickets map[string]string // normalized title -> key
	order   []string
}

// New returns a ledger seeded with the incident #418 scenario of the lecture.
func New() *Ledger { return &Ledger{tickets: map[string]string{}} }

var payouts = map[string][]string{
	"a-114": {
		"batch #422 pending since 03:10 (EUR 18,240.00)",
		"batch #421 pending since 03:10 (EUR 9,115.50)",
		"batch #418 settled late 02:41 (EUR 31,002.00)",
		"batch #417 settled 01:05 (EUR 12,880.00)",
		"batch #416 settled 00:12 (EUR 4,310.25)",
		"batch #415 settled 22:48 (EUR 27,991.00)",
		"batch #414 settled 21:30 (EUR 6,540.00)",
		"batch #413 settled 20:02 (EUR 15,020.75)",
	},
}

// SearchWiki is a read-only keyword search over runbooks and postmortems.
func (l *Ledger) SearchWiki(query string) string {
	q := strings.ToLower(query)
	if strings.Contains(q, "payout") || strings.Contains(q, "418") {
		return "3 hits: event-1 (rejected), event-2 (merged into incident #418), event-3 (open)"
	}
	return "0 hits"
}

// Payouts is a read-only page of a merchant's recent payouts. rows<=0 means
// DefaultPayoutRows; the tail is summarized, not dropped silently.
func (l *Ledger) Payouts(merchantID, since string, rows int) (string, error) {
	list, ok := payouts[strings.ToLower(strings.TrimSpace(merchantID))]
	if !ok {
		return "", fmt.Errorf("%w: %q (known merchants look like A-114)", ErrMerchantNotFound, merchantID)
	}
	if rows <= 0 {
		rows = DefaultPayoutRows
	}
	shown := list
	if rows < len(list) {
		shown = list[:rows]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "2 payouts pending since 03:10; last settled batch #418 delayed, acquirer says unrelated (since %s)", since)
	for _, r := range shown {
		b.WriteString("\n- " + r)
	}
	if more := len(list) - len(shown); more > 0 {
		fmt.Fprintf(&b, "\n... %d older rows not shown; pass rows=%d to see all", more, len(list))
	}
	return b.String(), nil
}

// CreateTicket is the side-effecting call. The same title returns the same key.
func (l *Ledger) CreateTicket(title, body string) (string, error) {
	norm := strings.ToLower(strings.TrimSpace(title))
	if norm == "" {
		return "", errors.New("ticket title is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if key, ok := l.tickets[norm]; ok {
		return key, nil
	}
	key := fmt.Sprintf("LDG-%d", len(l.order)+1)
	l.tickets[norm] = key
	l.order = append(l.order, key+": "+strings.TrimSpace(title)+" / "+body)
	return key, nil
}

// Tickets returns the created tickets, for the demo and tests.
func (l *Ledger) Tickets() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.order...)
}

// Typed arguments. The jsonschema tags are what ADK's functiontool turns into
// the JSON schema the model sees; the hand-written genai.Schemas in Tools say
// the same thing for the loop that has no framework.

// WikiArgs is search_internal_wiki's input.
type WikiArgs struct {
	Query string `json:"query" jsonschema:"what to look for in the internal wiki"`
}

// PayoutsArgs is get_merchant_payouts' input.
type PayoutsArgs struct {
	MerchantID string `json:"merchant_id" jsonschema:"merchant identifier, e.g. A-114"`
	Since      string `json:"since,omitempty" jsonschema:"lower time bound, e.g. 03:00"`
	Rows       int    `json:"rows,omitempty" jsonschema:"how many rows to return; default 3"`
}

// TicketArgs is create_jira_ticket's input.
type TicketArgs struct {
	Title string `json:"title" jsonschema:"ticket summary"`
	Body  string `json:"body,omitempty" jsonschema:"ticket description"`
}

// Tools returns the three tools for the framework-free loop.
func (l *Ledger) Tools() react.Registry {
	str := func(desc string) *genai.Schema { return &genai.Schema{Type: genai.TypeString, Description: desc} }
	obj := func(required []string, props map[string]*genai.Schema) *genai.Schema {
		return &genai.Schema{Type: genai.TypeObject, Properties: props, Required: required}
	}
	return react.NewRegistry(
		react.Tool{
			Name:        "search_internal_wiki",
			Description: "Searches the internal wiki (runbooks, postmortems) and returns the matching entries.",
			Params:      obj([]string{"query"}, map[string]*genai.Schema{"query": str("what to look for")}),
			Call: func(_ context.Context, a map[string]any) (string, error) {
				return l.SearchWiki(Str(a, "query")), nil
			},
		},
		react.Tool{
			Name:        "get_merchant_payouts",
			Description: "Reads the most recent payouts of one merchant. Returns a page; pass rows to see more.",
			Params: obj([]string{"merchant_id"}, map[string]*genai.Schema{
				"merchant_id": str("merchant identifier, e.g. A-114"),
				"since":       str("lower time bound, e.g. 03:00"),
				"rows":        {Type: genai.TypeInteger, Description: "how many rows to return; default 3"},
			}),
			Call: func(_ context.Context, a map[string]any) (string, error) {
				return l.Payouts(Str(a, "merchant_id"), Str(a, "since"), Int(a, "rows"))
			},
		},
		react.Tool{
			Name:          "create_jira_ticket",
			Description:   "Creates a follow-up Jira ticket and returns its key (LDG-N).",
			SideEffecting: true,
			Params: obj([]string{"title"}, map[string]*genai.Schema{
				"title": str("ticket summary"), "body": str("ticket description")}),
			Call: func(_ context.Context, a map[string]any) (string, error) {
				return l.CreateTicket(Str(a, "title"), Str(a, "body"))
			},
		},
	)
}

// Str reads a string argument without panicking on a missing or mistyped key —
// model output is untrusted input.
func Str(a map[string]any, key string) string {
	s, _ := a[key].(string)
	return s
}

// Int reads a numeric argument; JSON numbers arrive as float64.
func Int(a map[string]any, key string) int {
	switch v := a[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
