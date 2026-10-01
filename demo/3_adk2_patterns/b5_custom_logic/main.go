// B5 · Custom Logic — the orchestration is ordinary Go (loops, if, error
// handling) inside a dynamic node, yet every child run stays durable and
// observable: successful child activations are cached and skipped on replay.
//
//	Start → checkout (DynamicNode) ──RunNode──→ price_line ×N  (one per order line)
//	                               └─→ notify (LlmAgent)
//
//	go run .
//	go run . -input "ORD-7: 1x gadget, 0x widget"
//	go run . -live
package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:    "B5",
	Title: "Custom Logic",
	Input: "ORD-9: 2x widget, 1x gadget, 3x unobtainium, 1x widget",
	Build: build,
}

func main() { kit.Main(spec) }

// catalog is the price list in cents.
var catalog = map[string]int{"widget": 450, "gadget": 1299}

// line is one order line, the input of a price_line child.
type line struct {
	N   int    `json:"n"`
	SKU string `json:"sku"`
	Qty int    `json:"qty"`
}

// priced is a child's output.
type priced struct {
	SKU   string `json:"sku"`
	Cents int    `json:"cents"`
}

// summary is the dynamic node's output.
type summary struct {
	Order   string   `json:"order"`
	Cents   int      `json:"total_cents"`
	Lines   int      `json:"lines_ok"`
	Skipped []string `json:"skipped,omitempty"`
}

var errUnknownSKU = errors.New("unknown sku")

// priceLine prices one line. It is a child node, so each call is a separate,
// cached activation in the session.
func priceLine(_ agent.Context, l line) (priced, error) {
	unit, ok := catalog[l.SKU]
	if !ok {
		return priced{}, fmt.Errorf("%w %q", errUnknownSKU, l.SKU)
	}
	return priced{SKU: l.SKU, Cents: unit * l.Qty}, nil
}

// parse turns "ORD-9: 2x widget, 1x gadget" into an order id and lines.
// Malformed lines are reported, not guessed.
func parse(in string) (string, []line, []string) {
	order, rest, ok := strings.Cut(in, ":")
	if !ok {
		order, rest = "ORD-?", in
	}
	var lines []line
	var bad []string
	for i, raw := range strings.Split(rest, ",") {
		raw = strings.TrimSpace(raw)
		qtyStr, sku, ok := strings.Cut(raw, "x ")
		qty, err := strconv.Atoi(strings.TrimSpace(qtyStr))
		if !ok || err != nil || qty <= 0 {
			bad = append(bad, fmt.Sprintf("line %d %q: bad quantity", i+1, raw))
			continue
		}
		lines = append(lines, line{N: i + 1, SKU: strings.TrimSpace(sku), Qty: qty})
	}
	return strings.TrimSpace(order), lines, bad
}

// checkout is the body of the dynamic node: plain Go control flow.
func checkout(pricer workflow.Node) workflow.DynamicFn[string, summary] {
	return func(ctx agent.Context, in string, _ func(*session.Event) error) (summary, error) {
		order, lines, bad := parse(in)
		s := summary{Order: order, Skipped: bad}
		for _, l := range lines {
			// WithRunID makes the child's identity stable ("price_line@line-2"),
			// so a resumed run replays its cached result instead of re-running it.
			p, err := workflow.RunNode[priced](ctx, pricer, l, workflow.WithRunID(fmt.Sprintf("line-%d", l.N)))
			if err != nil {
				// Error handling is ordinary Go: skip the line, keep the order.
				s.Skipped = append(s.Skipped, fmt.Sprintf("line %d: %s", l.N, reason(err)))
				continue
			}
			s.Cents += p.Cents
			s.Lines++
		}
		if s.Lines == 0 {
			return summary{}, fmt.Errorf("order %s: no valid lines (%s)", order, strings.Join(s.Skipped, "; "))
		}
		return s, nil
	}
}

// reason names why a child failed. A skipped line that does not say why is
// a silent failure with extra steps.
//
// In v2.5.0 RunNode does NOT keep the child's error chain: errors.Is(err,
// errUnknownSKU) is false; only workflow.ErrNodeFailed survives, and the
// child's message is flattened into the text after "dynamic child failed: ".
func reason(err error) string {
	if !errors.Is(err, workflow.ErrNodeFailed) {
		return err.Error()
	}
	msg := err.Error()
	if i := strings.LastIndex(msg, "dynamic child failed: "); i >= 0 {
		return msg[i+len("dynamic child failed: "):]
	}
	return msg
}

func notifyBrain(p kit.Prompt) kit.Reply {
	return kit.Say("Order confirmed. Details: %s", p.Text)
}

func build(m kit.Models) (agent.Agent, error) {
	notifier, err := llmagent.New(llmagent.Config{
		Name:        "notify",
		Description: "Writes the customer confirmation.",
		Model:       m.For("notify", notifyBrain),
		Instruction: `You get an order summary as JSON (order, total_cents, lines_ok, skipped).
Write a two-sentence confirmation for the customer: the total in EUR, and which lines were skipped and why.`,
	})
	if err != nil {
		return nil, err
	}
	notify, err := workflow.NewAgentNode(notifier, workflow.NodeConfig{})
	if err != nil {
		return nil, err
	}
	pricer := workflow.NewFunctionNode("price_line", priceLine, workflow.NodeConfig{})
	return workflowagent.New(workflowagent.Config{
		Name:        "checkout_flow",
		Description: "Dynamic node pricing each order line.",
		Edges: workflow.Chain(
			workflow.Start,
			workflow.NewDynamicNode("checkout", checkout(pricer), workflow.NodeConfig{}),
			notify,
		),
	})
}
