package main

import (
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// NewRateTool builds the typed function tool.
//
// The schema is INFERRED from RateInput/RateOutput. Note what is not here: no
// hand-written JSON Schema, and no constraint syntax in the struct tags. If you
// need constraints (enum, minimum, required beyond Go's zero values), set
// functiontool.Config.InputSchema explicitly with a *jsonschema.Schema.
func NewRateTool(p Provider) (tool.Tool, error) {
	handler := func(ctx agent.Context, in RateInput) (RateOutput, error) {
		return Convert(ctx, p, in)
	}
	t, err := functiontool.New(functiontool.Config{
		Name: "get_exchange_rate",
		Description: "Returns the exchange rate between two ISO 4217 currencies, " +
			"cross-rated through UAH using National Bank of Ukraine data.",
	}, handler)
	if err != nil {
		return nil, fmt.Errorf("build rate tool: %w", err)
	}
	return t, nil
}

// instruction is deliberately explicit about the failure path.
//
// "Prompt vs contract": this text steers behaviour, but it guarantees nothing.
// The JSON Schema and the typed Go handler are what actually stop a malformed
// call from reaching the rate provider. Teach both, trust only the second.
const instruction = `You answer currency questions for Ukrainian users.

Rules:
- Always call get_exchange_rate for any rate question. Never state a rate from memory.
- The tool returns a source and a date; include both in your answer.
- If the tool returns an error naming an invalid or unknown currency code, correct
  your arguments and call it again at most once. If it still fails, say plainly
  that you could not get the rate. Never invent a number.`

// NewAgent wires the model and tool into an LlmAgent.
//
// Tool surface (least agency in action): the local typed rate tool is
// always there. External tools are added only when they resolved at startup —
// the model sees exactly the tools we wired, never a "maybe". Pass
// extraTools=nil for the core lab shape (one local tool), which is what tests
// do.
//
// extraTools are already-expanded tool.Tool values, not toolsets. The eager
// expansion happens in ResolveToolsets so the web UI graph can see them — the
// graph draws Reveal(agent).Tools and never .Toolsets. Keeping the toolsets
// out of Toolsets is deliberate: tools_processor.go appends toolset tools
// onto Tools at call time, so listing the same tool in both places makes the
// request carry it twice and PackTool fails the run with `duplicate tool`.
func NewAgent(m model.LLM, p Provider, extraTools ...tool.Tool) (agent.Agent, error) {
	rateTool, err := NewRateTool(p)
	if err != nil {
		return nil, err
	}
	tools := []tool.Tool{rateTool}
	tools = append(tools, extraTools...)
	a, err := llmagent.New(llmagent.Config{
		Name:        "currency_agent",
		Model:       m,
		Description: "Answers currency-exchange questions using live exchange rates (NBU or monobank).",
		Instruction: instruction,
		Tools:       tools,
	})
	if err != nil {
		return nil, fmt.Errorf("build agent: %w", err)
	}
	return a, nil
}
