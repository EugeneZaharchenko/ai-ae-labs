// Package scripted is a model.LLM test double for the Day 7 lab.
//
// internal/fakellm answers one function call per turn and reports no token
// usage. A ReAct lab needs both: several calls in ONE model turn (the thing
// parallel dispatch exists for) and UsageMetadata (the thing a token budget is
// charged from). Lab-local, because it is used only here.
package scripted

import (
	"context"
	"errors"
	"iter"
	"sync"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// ErrExhausted: the agent asked for more turns than the script has.
var ErrExhausted = errors.New("scripted: script exhausted")

// Turn is one scripted model response.
type Turn struct {
	Text  string
	Calls []*genai.FunctionCall
	Usage *genai.GenerateContentResponseUsageMetadata
	Err   error
}

// Say is a final-answer turn.
func Say(text string, tokens int32) Turn {
	return Turn{Text: text, Usage: usage(tokens)}
}

// Call is a turn that requests the given calls together.
func Call(tokens int32, calls ...*genai.FunctionCall) Turn {
	return Turn{Calls: calls, Usage: usage(tokens)}
}

// FC builds a function call.
func FC(id, name string, args map[string]any) *genai.FunctionCall {
	return &genai.FunctionCall{ID: id, Name: name, Args: args}
}

func usage(total int32) *genai.GenerateContentResponseUsageMetadata {
	prompt := total * 3 / 4
	return &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: prompt, CandidatesTokenCount: total - prompt, TotalTokenCount: total,
	}
}

// Model replays turns in order. With Loop set, the last turn repeats forever —
// the runaway model a step cap exists for.
type Model struct {
	name string
	loop bool

	mu       sync.Mutex
	turns    []Turn
	pos      int
	requests []*model.LLMRequest
}

// New returns a model that plays turns once.
func New(name string, turns ...Turn) *Model { return &Model{name: name, turns: turns} }

// Forever returns a model that plays turn on every call.
func Forever(name string, turn Turn) *Model {
	return &Model{name: name, turns: []Turn{turn}, loop: true}
}

// Name implements model.LLM.
func (m *Model) Name() string { return m.name }

// GenerateContent implements model.LLM.
func (m *Model) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	var turn Turn
	exhausted := false
	switch {
	case m.pos < len(m.turns):
		turn = m.turns[m.pos]
		m.pos++
	case m.loop:
		turn = m.turns[len(m.turns)-1]
	default:
		exhausted = true
	}
	m.mu.Unlock()

	return func(yield func(*model.LLMResponse, error) bool) {
		switch {
		case exhausted:
			yield(nil, ErrExhausted)
		case turn.Err != nil:
			yield(nil, turn.Err)
		default:
			c := &genai.Content{Role: genai.RoleModel}
			if turn.Text != "" {
				c.Parts = append(c.Parts, &genai.Part{Text: turn.Text})
			}
			for _, fc := range turn.Calls {
				cp := *fc
				c.Parts = append(c.Parts, &genai.Part{FunctionCall: &cp})
			}
			yield(&model.LLMResponse{Content: c, UsageMetadata: turn.Usage, TurnComplete: true}, nil)
		}
	}
}

// Requests returns every request received, in order.
func (m *Model) Requests() []*model.LLMRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*model.LLMRequest(nil), m.requests...)
}

// Calls reports how many times the model was called.
func (m *Model) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}
