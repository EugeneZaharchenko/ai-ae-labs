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
	statusTool, err := refund.NewStatusTool(reg)
	if err != nil {
		return nil, err
	}
	return llmagent.New(llmagent.Config{
		Name:  refund.AppName,
		Model: m,
		// Both tools are declared, and the instruction says which one answers
		// a status question. Declaring only the opener was the bug: a model
		// with no read tool has nothing to read with, so it refuses the
		// request instead of answering it.
		Instruction: `You handle LEDGERWORKS refund cases only, two ways.
+ A request with transaction and merchant IDs asks to open a case: call open_refund_case.
+ A request about an existing case asks for its status: call check_refund_status with the case ID.
+ Never claim a case was opened or has a status without a successful tool result.
+ Report the case ID and status from the tool in Ukrainian.
+ If a status request names no case ID, ask for it; never guess one from a transaction ID.
+ Opening a case does not transfer money. Refuse unrelated requests.`,
		Tools: []tool.Tool{refundTool, statusTool},
	})
}
