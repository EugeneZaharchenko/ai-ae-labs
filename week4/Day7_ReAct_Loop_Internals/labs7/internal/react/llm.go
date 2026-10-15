package react

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// SystemPrompt is the instruction of the LLM planner. It states the job and
// the stop rule; the tool list travels as function declarations, not as prose.
const SystemPrompt = `You are the investigation agent of LEDGERWORKS, a payment processor.
Use the tools to gather evidence. Call independent read-only tools together in ONE step.
Never invent identifiers: use only merchant and incident ids that appear in the task or in tool results.
When you have enough evidence, answer in two sentences and call no tool.
You only handle LEDGERWORKS payout incidents; refuse anything else.`

// LLMPlanner asks a real model for the next step through native function
// calling: tools are declared to the provider, the provider answers with
// structured calls, and the provider reports what it spent.
//
// This is the part ADK's llmagent does for you; writing it once by hand is how
// you learn what the framework is hiding.
type LLMPlanner struct {
	Model  model.LLM
	Tools  Registry
	Effort Effort
}

// NextStep implements Planner.
func (p LLMPlanner) NextStep(ctx context.Context, h History) (Step, error) {
	req := &model.LLMRequest{
		Model:    p.Model.Name(),
		Contents: contentsFrom(h),
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText(SystemPrompt, genai.RoleUser),
			Tools:             []*genai.Tool{{FunctionDeclarations: p.declarations()}},
			ToolConfig: &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{
				Mode: genai.FunctionCallingConfigModeAuto,
			}},
			Temperature: genai.Ptr[float32](0),
		},
	}
	if tc := thinkingFor(p.Effort); tc != nil {
		req.Config.ThinkingConfig = tc
	}

	resp, err := lastResponse(p.Model.GenerateContent(ctx, req, false))
	if err != nil {
		return Step{}, fmt.Errorf("model %s: %w", p.Model.Name(), err)
	}
	return stepFrom(resp, countAssistant(h)+1), nil
}

func thinkingFor(e Effort) *genai.ThinkingConfig {
	switch e {
	case EffortLow:
		return &genai.ThinkingConfig{ThinkingBudget: genai.Ptr[int32](0)}
	case EffortHigh:
		return &genai.ThinkingConfig{ThinkingBudget: genai.Ptr[int32](4096)}
	default:
		return nil
	}
}

func (p LLMPlanner) declarations() []*genai.FunctionDeclaration {
	out := make([]*genai.FunctionDeclaration, 0, len(p.Tools))
	for _, name := range p.Tools.Names() {
		t := p.Tools[name]
		desc := t.Description
		if t.SideEffecting {
			desc += " Changes state in the outside world; call it at most once per case."
		}
		out = append(out, &genai.FunctionDeclaration{Name: t.Name, Description: desc, Parameters: t.Params})
	}
	return out
}

// lastResponse drains a (possibly streaming) response and keeps the final
// complete one; usage is taken from whichever chunk carries it.
func lastResponse(seq func(yield func(*model.LLMResponse, error) bool)) (*model.LLMResponse, error) {
	var last *model.LLMResponse
	var usage *genai.GenerateContentResponseUsageMetadata
	for r, err := range seq {
		if err != nil {
			return nil, err
		}
		if r == nil {
			continue
		}
		if r.UsageMetadata != nil {
			usage = r.UsageMetadata
		}
		if !r.Partial || last == nil {
			last = r
		}
	}
	if last == nil {
		return nil, errors.New("model returned no response")
	}
	out := *last
	out.UsageMetadata = usage
	return &out, nil
}

// stepFrom turns a model response into a Step. n is the 1-based step number,
// used to mint IDs when the provider sent none.
func stepFrom(resp *model.LLMResponse, n int) Step {
	step := Step{Usage: UsageFrom(resp.UsageMetadata)}
	if resp.Content == nil {
		return step
	}
	raw := cloneContent(resp.Content)
	var text, thought []string
	for _, part := range raw.Parts {
		switch {
		case part == nil:
		case part.FunctionCall != nil:
			fc := part.FunctionCall
			if fc.ID == "" {
				fc.ID = fmt.Sprintf("s%d-c%d", n, len(step.Calls)+1)
			}
			step.Calls = append(step.Calls, ToolCall{ID: fc.ID, Name: fc.Name, Args: fc.Args})
		case part.Text != "" && part.Thought:
			thought = append(thought, part.Text)
		case part.Text != "":
			text = append(text, part.Text)
		}
	}
	step.Raw = raw
	step.Thought = strings.TrimSpace(strings.Join(append(thought, ifCalls(text, step.Calls)...), " "))
	if len(step.Calls) == 0 && len(text) > 0 {
		step.Final = true
		step.Answer = strings.TrimSpace(strings.Join(text, " "))
	}
	return step
}

// ifCalls: text next to function calls is the model narrating its plan, so it
// is the Thought; text without calls is the answer and is handled separately.
func ifCalls(text []string, calls []ToolCall) []string {
	if len(calls) == 0 {
		return nil
	}
	return text
}

func cloneContent(c *genai.Content) *genai.Content {
	out := &genai.Content{Role: c.Role, Parts: make([]*genai.Part, len(c.Parts))}
	for i, p := range c.Parts {
		if p == nil {
			continue
		}
		cp := *p
		if p.FunctionCall != nil {
			fc := *p.FunctionCall
			cp.FunctionCall = &fc
		}
		out.Parts[i] = &cp
	}
	return out
}

func countAssistant(h History) int {
	n := 0
	for _, m := range h {
		if m.Role == RoleAssistant {
			n++
		}
	}
	return n
}

// contentsFrom converts the lab's history into provider messages. The model's
// own turn is replayed verbatim (Raw) when we have it; a tool message becomes
// one user-role content of function responses, each carrying its CallID.
func contentsFrom(h History) []*genai.Content {
	var out []*genai.Content
	for _, m := range h {
		switch m.Role {
		case RoleUser:
			out = append(out, genai.NewContentFromText(m.Content, genai.RoleUser))
		case RoleAssistant:
			out = append(out, assistantContent(m))
		case RoleTool:
			parts := make([]*genai.Part, 0, len(m.Results))
			for _, r := range m.Results {
				resp := map[string]any{"output": r.Content}
				if r.Err != nil {
					resp = map[string]any{"error": r.Err.Error()}
				}
				parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{
					ID: r.CallID, Name: r.Tool, Response: resp}})
			}
			out = append(out, &genai.Content{Role: genai.RoleUser, Parts: parts})
		}
	}
	return out
}

func assistantContent(m Message) *genai.Content {
	if m.Raw != nil {
		return m.Raw
	}
	c := &genai.Content{Role: genai.RoleModel}
	if m.Content != "" {
		c.Parts = append(c.Parts, &genai.Part{Text: m.Content})
	}
	for _, call := range m.Calls {
		c.Parts = append(c.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: call.ID, Name: call.Name, Args: call.Args}})
	}
	return c
}
