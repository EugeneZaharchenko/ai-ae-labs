package main

import (
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"

	"github.com/dimetron/ai-eng-course/labs/week2/internal/refund"
)

func newLiveAgent(m model.LLM, reg *refund.Registry) (agent.Agent, error) {
	refundTool, err := refund.NewTool(reg)
	if err != nil {
		return nil, err
	}
	return llmagent.New(llmagent.Config{
		Name:  refund.AppName,
		Model: m,
		Instruction: `You handle LEDGERWORKS refund case requests only.
For a request containing transaction and merchant IDs, call open_refund_case.
Never claim a case was opened without a successful tool result.
Report the case ID and status from the tool in Ukrainian.
Opening a case does not transfer money. Ask for missing IDs; refuse unrelated requests.`,
		Tools: []tool.Tool{refundTool},
	})
}
