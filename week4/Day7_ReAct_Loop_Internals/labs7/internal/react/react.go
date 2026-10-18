// Package react is the vocabulary of the Day 7 lab: the types a ReAct loop is
// written in, the tool registry it dispatches to, and two Planners — a
// deterministic one for offline runs and an LLM-backed one that uses the
// provider's native function calling.
//
// It is plumbing, not the assignment. The assignment is the loop itself
// (react.go, graph.go in the lab package): when to stop, what to charge, how
// to order side effects. Nothing here imports the ADK workflow package, so the
// "manual loop" stays free of any framework.
package react

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"
)

// ErrUnknownTool: the model asked for a tool that is not registered. It is
// returned inside an Observation, not as a loop failure — the model gets a
// chance to correct itself.
var ErrUnknownTool = errors.New("unknown tool")

// ToolCall is one request from the model. ID correlates the result with it.
//
// With native function calling the provider usually supplies the ID
// (OpenAI call_id, Gemini function_call.id); when it does not, the planner
// assigns a deterministic one, so correlation never depends on the model.
type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// Usage is what the provider reported for ONE model call
// (genai UsageMetadata). Thoughts are billed as output on most providers, so a
// "high reasoning effort" step is visible here as a larger Thoughts number.
type Usage struct {
	Prompt   int `json:"prompt"`
	Output   int `json:"output"`
	Thoughts int `json:"thoughts"`
}

// Total is what the loop charges against Budget.MaxTokens.
func (u Usage) Total() int { return u.Prompt + u.Output + u.Thoughts }

// Add sums two usages.
func (u Usage) Add(o Usage) Usage {
	return Usage{u.Prompt + o.Prompt, u.Output + o.Output, u.Thoughts + o.Thoughts}
}

// UsageFrom converts genai usage metadata; nil means the provider reported
// nothing, which is a zero Usage, not an error.
func UsageFrom(m *genai.GenerateContentResponseUsageMetadata) Usage {
	if m == nil {
		return Usage{}
	}
	return Usage{
		Prompt:   int(m.PromptTokenCount + m.ToolUsePromptTokenCount),
		Output:   int(m.CandidatesTokenCount),
		Thoughts: int(m.ThoughtsTokenCount),
	}
}

// Step is one Reason → Act decision. Calls holds zero or more tool calls;
// Final=true with Answer ends the loop.
type Step struct {
	Thought string
	Calls   []ToolCall
	Final   bool
	Answer  string
	// Usage is what this single planning step cost. A planner that cannot
	// report usage (the rule-based one) leaves it zero.
	Usage Usage
	// Raw is the model's own turn, verbatim. It is kept because some
	// providers (Gemini 3 thought signatures) require the exact assistant
	// turn to be replayed next to its function responses.
	Raw *genai.Content
}

// Observation is the result of one call, tied to it by CallID. Err is a fact
// about the outside world and goes back to the model; it does not end the loop.
type Observation struct {
	CallID  string
	Tool    string
	Content string
	Err     error
}

// String renders the observation for traces.
func (o Observation) String() string {
	if o.Err != nil {
		return fmt.Sprintf("[%s] %s: ERROR: %v", o.CallID, o.Tool, o.Err)
	}
	return fmt.Sprintf("[%s] %s: %s", o.CallID, o.Tool, o.Content)
}

// Role of a history message.
type Role string

// Roles. Tool results are NOT user messages: a function response is a
// separate, structurally typed part, so text a tool returned cannot pose as
// an instruction from the human.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one history entry.
type Message struct {
	Role    Role
	Content string
	Calls   []ToolCall    // assistant: what the model asked for
	Results []Observation // tool: answers, each carrying its CallID
	Raw     *genai.Content
}

// History is what a Planner sees.
type History []Message

// Seed starts a history with the task.
func Seed(task string) History {
	return History{{Role: RoleUser, Content: task}}
}

// Append records a step and its observations. Observations are matched to the
// step's calls by CallID, not by position: the order in which a parallel
// dispatcher finishes is not the order in which the model asked.
func (h History) Append(step Step, obs []Observation) History {
	h = append(h, Message{Role: RoleAssistant, Content: step.Thought, Calls: step.Calls, Raw: step.Raw})
	byID := make(map[string]Observation, len(obs))
	for _, o := range obs {
		byID[o.CallID] = o
	}
	results := make([]Observation, 0, len(step.Calls))
	for _, c := range step.Calls {
		if o, ok := byID[c.ID]; ok {
			results = append(results, o)
		}
	}
	return append(h, Message{Role: RoleTool, Results: results})
}

// Planner is all the loop knows about the model: "here is the history, say
// what happens next". One method keeps the loop testable without a network.
type Planner interface {
	NextStep(ctx context.Context, h History) (Step, error)
}

// Effort is the reasoning-effort knob of the model. It is a request setting
// whose cost shows up in Usage.Thoughts — which is why the loop budget is
// counted in tokens and not in iterations.
type Effort string

// Efforts. Empty means "provider default": no thinking configuration is sent.
const (
	EffortDefault Effort = ""
	EffortLow     Effort = "low"
	EffortHigh    Effort = "high"
)

// Budget collects every limit of a loop. They catch different failures:
// MaxIterations — a loop that never converges; MaxTokens — few steps that are
// each enormous; MaxDuration — one step stuck on a slow dependency.
type Budget struct {
	// MaxIterations is a step cap, independent of cost. Zero is replaced by
	// DefaultMaxIterations: "unlimited" is not a valid setting.
	MaxIterations int
	// MaxTokens is the total of Usage.Total() over the loop; zero = no cap.
	// It is checked BEFORE each model call and charged AFTER it, so a run can
	// overshoot by at most one step.
	MaxTokens int
	// MaxDuration is a wall-clock cap for the whole loop; zero = none.
	MaxDuration time.Duration
	// Effort is passed to the planner; it does not change the loop itself.
	Effort Effort
}

// DefaultMaxIterations is the step cap used when Budget leaves it zero.
const DefaultMaxIterations = 10

// Normalized returns the budget with safe defaults instead of zeros.
func (b Budget) Normalized() Budget {
	if b.MaxIterations <= 0 {
		b.MaxIterations = DefaultMaxIterations
	}
	return b
}

// Trace is everything a loop reports, including on failure: the question asked
// at 03:00 is "where did it get stuck", and only the trace answers it.
type Trace struct {
	Steps        []Step
	Observations [][]Observation
	Iterations   int
	Usage        Usage
}

// Tool is one capability the loop can dispatch to.
type Tool struct {
	Name        string
	Description string
	// SideEffecting is the engineer's declaration, not the model's: only the
	// person who wrote the tool knows whether it changes the outside world.
	SideEffecting bool
	Params        *genai.Schema
	Call          func(ctx context.Context, args map[string]any) (string, error)
}

// Registry maps names to tools.
type Registry map[string]Tool

// NewRegistry builds a registry.
func NewRegistry(tools ...Tool) Registry {
	r := make(Registry, len(tools))
	for _, t := range tools {
		r[t.Name] = t
	}
	return r
}

// Names returns the sorted tool names.
func (r Registry) Names() []string {
	out := make([]string, 0, len(r))
	for n := range r {
		out = append(out, n)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ToolCallTimeout bounds ONE tool call; a var so tests can shrink it.
var ToolCallTimeout = 30 * time.Second

// Invoke runs one call and turns every failure into an Observation: an unknown
// tool, an error, a timeout and a panic are facts for the model, not crashes of
// the loop.
func (r Registry) Invoke(ctx context.Context, c ToolCall) Observation {
	obs := Observation{CallID: c.ID, Tool: c.Name}
	t, ok := r[c.Name]
	if !ok {
		obs.Err = fmt.Errorf("%w: %q (available: %s)", ErrUnknownTool, c.Name, strings.Join(r.Names(), ", "))
		return obs
	}
	callCtx, cancel := context.WithTimeout(ctx, ToolCallTimeout)
	defer cancel()
	content, err := callSafely(callCtx, t, c.Args)
	if err != nil {
		obs.Err = err
		return obs
	}
	obs.Content = content
	return obs
}

func callSafely(ctx context.Context, t Tool, args map[string]any) (out string, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("tool %q panicked: %v", t.Name, p)
		}
	}()
	return t.Call(ctx, args)
}
