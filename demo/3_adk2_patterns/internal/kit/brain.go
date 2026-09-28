// Package kit is the shared harness of the ADK 2.x pattern catalog: a
// rule-based offline model, a trace printer and the entry point every pattern
// main.go calls.
//
// Why rule-based and not a replayed script (internal/fakellm): a catalog demo
// is also run interactively (`go run . console`), where the input is whatever
// the reader types. A Brain looks at the request and decides, so the same
// agent answers any input deterministically and without a key.
//
// Verified against google.golang.org/adk/v2 v2.4.0 (станом на 09/2026).
package kit

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// Prompt is what a Brain sees of one model request.
type Prompt struct {
	// Agent is the name the model was created for.
	Agent string
	// Text is the latest user-side text in the request: the chat message, or
	// the node input a workflow placed in front of a single-turn agent.
	Text string
	// Results holds the function responses of the current turn (after the
	// latest user text), by tool name. The last response for a name wins.
	Results map[string]map[string]any
	// Called lists the tool names requested in the current turn, in order.
	Called []string
}

// Result returns the response of the named tool and whether it exists.
func (p Prompt) Result(tool string) (map[string]any, bool) {
	r, ok := p.Results[tool]
	return r, ok
}

// Has reports whether Text contains any of the words, case-insensitively.
func (p Prompt) Has(words ...string) bool {
	low := strings.ToLower(p.Text)
	for _, w := range words {
		if strings.Contains(low, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

// Reply is one model turn. Set exactly one of Text, Call or Err.
type Reply struct {
	Text string
	Call string
	Args map[string]any
	Err  error
}

// Say is a text reply.
func Say(format string, a ...any) Reply { return Reply{Text: fmt.Sprintf(format, a...)} }

// Call is a function-call reply.
func Call(tool string, args map[string]any) Reply { return Reply{Call: tool, Args: args} }

// Brain decides a reply from a prompt. It must be a pure function of the
// prompt: that is what makes an offline run repeatable.
type Brain func(Prompt) Reply

// Model is a model.LLM driven by a Brain.
type Model struct {
	name  string
	brain Brain

	mu    sync.Mutex
	calls int
}

var _ model.LLM = (*Model)(nil)

// NewModel returns an offline model named name.
func NewModel(name string, brain Brain) *Model { return &Model{name: name, brain: brain} }

// Name implements model.LLM.
func (m *Model) Name() string { return "offline/" + m.name }

// Calls reports how many requests the model served. Patterns on the "model
// decides" side of the axis cost more calls; tests assert that number.
func (m *Model) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// GenerateContent implements model.LLM.
func (m *Model) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()

	reply := m.brain(Read(m.name, req))
	return func(yield func(*model.LLMResponse, error) bool) {
		if reply.Err != nil {
			yield(nil, reply.Err)
			return
		}
		part := genai.NewPartFromText(reply.Text)
		if reply.Call != "" {
			part = genai.NewPartFromFunctionCall(reply.Call, reply.Args)
		}
		yield(&model.LLMResponse{
			Content:      &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{part}},
			TurnComplete: true,
		}, nil)
	}
}

// Read turns an LLM request into a Prompt.
func Read(agent string, req *model.LLMRequest) Prompt {
	p := Prompt{Agent: agent, Results: map[string]map[string]any{}}
	if req == nil {
		return p
	}
	for _, c := range req.Contents {
		if c == nil {
			continue
		}
		var text strings.Builder
		for _, part := range c.Parts {
			switch {
			case part == nil:
			case part.FunctionCall != nil:
				p.Called = append(p.Called, part.FunctionCall.Name)
			case part.FunctionResponse != nil:
				p.Results[part.FunctionResponse.Name] = part.FunctionResponse.Response
			case part.Text != "":
				text.WriteString(part.Text)
			}
		}
		if c.Role != genai.RoleModel && strings.TrimSpace(text.String()) != "" {
			// A new user message starts a new turn: tool results and calls
			// from earlier turns in the same session are not this turn's.
			p.Text = strings.TrimSpace(text.String())
			p.Results = map[string]map[string]any{}
			p.Called = nil
		}
	}
	return p
}
