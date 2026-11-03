// Package scripted is a model.LLM that replays a fixed script.
//
// Every week-4-onward example calls a real model, and every one of them must
// still be testable with no API key and no network — so each example takes a
// model.LLM and the test hands it this one. The graph, the loop, the tool
// dispatch and the stop rule are all exercised for real; only the intelligence
// is scripted.
//
// It is a test double, and calling it anything else would be the dishonesty
// this whole module is trying to avoid: it proves the wiring, not the answer.
//
// Same shape as the labs' internal/scripted (Day 7), so a learner who reads the
// example and then the lab sees one idea, not two.
package scripted

import (
	"context"
	"errors"
	"iter"
	"sync"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// ErrExhausted means the caller asked for more turns than the script has. A
// step cap in the caller turns this into a clean stop; without one it is a
// hung run, which is the point.
var ErrExhausted = errors.New("scripted: script exhausted")

// Turn is one scripted model response.
type Turn struct {
	Text  string
	Calls []*genai.FunctionCall
	Usage *genai.GenerateContentResponseUsageMetadata
	Err   error
}

// Say builds a final-answer turn: text and no calls, which is how a model ends
// a loop.
func Say(text string, tokens int32) Turn {
	return Turn{Text: text, Usage: usage(tokens)}
}

// Call builds a turn that requests every given call together. Calling several
// read-only tools in one turn is the normal case, not an optimisation.
func Call(tokens int32, calls ...*genai.FunctionCall) Turn {
	return Turn{Calls: calls, Usage: usage(tokens)}
}

// Fail builds a turn that returns an error, for exercising the caller's
// handling of a provider that falls over mid-run.
func Fail(err error) Turn { return Turn{Err: err} }

// FC builds a function call. The id must be echoed on the matching
// FunctionResponse; it is how a result is tied back to its request.
func FC(id, name string, args map[string]any) *genai.FunctionCall {
	return &genai.FunctionCall{ID: id, Name: name, Args: args}
}

// usage splits a made-up total so reported numbers look like a provider's.
func usage(total int32) *genai.GenerateContentResponseUsageMetadata {
	prompt := total * 3 / 4
	return &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: prompt, CandidatesTokenCount: total - prompt, TotalTokenCount: total,
	}
}

// Model replays turns in order. With Loop set, the last turn repeats forever —
// the runaway model that a step cap exists for.
type Model struct {
	name string
	loop bool

	mu       sync.Mutex
	turns    []Turn
	pos      int
	requests []*model.LLMRequest
}

// New returns a model that plays each turn once.
func New(name string, turns ...Turn) *Model { return &Model{name: name, turns: turns} }

// Forever returns a model that plays one turn on every call.
func Forever(name string, turn Turn) *Model {
	return &Model{name: name, turns: []Turn{turn}, loop: true}
}

// Name implements model.LLM.
func (m *Model) Name() string { return m.name }

// GenerateContent implements model.LLM.
//
// The returned sequence is drained by the caller with a plain
// `for resp, err := range seq` loop; nothing here blocks on a consumer.
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

// Requests returns every request the model received, in order — the only way a
// test can assert on what the loop actually asked for.
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
