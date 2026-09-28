// A1 · Single Agent — one LlmAgent, one prompt, a set of tools; the model
// picks which tool to call and in what order.
//
//	go run .              # scripted demo, offline
//	go run . -live        # same agent on the model from apps/.env
//	go run . console      # talk to it (offline brain answers any input)
package main

import (
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:    "A1",
	Title: "Single Agent",
	Input: "Where is my order ORD-42?",
	Build: build,
}

func main() { kit.Main(spec) }

type orderArgs struct {
	OrderID string `json:"order_id" jsonschema:"order id, e.g. ORD-42"`
}

type order struct {
	OrderID string `json:"order_id"`
	Status  string `json:"status"`
	Carrier string `json:"carrier,omitempty"`
}

type delivery struct {
	OrderID string `json:"order_id"`
	ETA     string `json:"eta"`
}

func getOrder(_ agent.Context, in orderArgs) (order, error) {
	if in.OrderID == "" {
		return order{}, fmt.Errorf("order_id is required")
	}
	return order{OrderID: in.OrderID, Status: "shipped", Carrier: "NovaPoshta"}, nil
}

func checkDelivery(_ agent.Context, in orderArgs) (delivery, error) {
	return delivery{OrderID: in.OrderID, ETA: "2026-10-01"}, nil
}

// brain is the offline stand-in for the model's own tool planning:
// look the order up, then check delivery, then answer.
func brain(p kit.Prompt) kit.Reply {
	id := orderID(p.Text)
	if id == "" {
		return kit.Say("Which order? Send an id like ORD-42.")
	}
	if _, ok := p.Result("get_order"); !ok {
		return kit.Call("get_order", map[string]any{"order_id": id})
	}
	if _, ok := p.Result("check_delivery"); !ok {
		return kit.Call("check_delivery", map[string]any{"order_id": id})
	}
	o, _ := p.Result("get_order")
	d, _ := p.Result("check_delivery")
	return kit.Say("Order %s is %v with %v, ETA %v.", id, o["status"], o["carrier"], d["eta"])
}

func orderID(text string) string {
	for _, f := range strings.Fields(text) {
		f = strings.Trim(f, "?.,!")
		if strings.HasPrefix(strings.ToUpper(f), "ORD-") {
			return strings.ToUpper(f)
		}
	}
	return ""
}

func build(m kit.Models) (agent.Agent, error) {
	getOrderTool, err := functiontool.New(functiontool.Config{
		Name:        "get_order",
		Description: "Look up an order by id.",
	}, getOrder)
	if err != nil {
		return nil, err
	}
	deliveryTool, err := functiontool.New(functiontool.Config{
		Name:        "check_delivery",
		Description: "Check the delivery ETA of an order.",
	}, checkDelivery)
	if err != nil {
		return nil, err
	}
	return llmagent.New(llmagent.Config{
		Name:        "support_agent",
		Description: "Answers order status questions.",
		Model:       m.For("support_agent", brain),
		Instruction: `You are a support assistant. For an order question, call get_order,
then check_delivery, then answer in one sentence with status, carrier and ETA.
Never invent data that a tool did not return.`,
		Tools: []tool.Tool{getOrderTool, deliveryTool},
	})
}
