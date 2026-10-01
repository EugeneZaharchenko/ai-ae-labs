// Package fakellm provides a scripted model.LLM double for tests.
//
// Why this exists in a course repo: agent behaviour is otherwise only
// observable by spending money on a real provider and hoping the model does the
// same thing twice. A scripted model makes tool-calling, retry and
// self-correction paths deterministic, so a lab can assert "the agent called
// get_rate with these arguments" instead of eyeballing a transcript.
//
// It implements model.LLM against ADK Go v2.5.0:
//
//	Name() string
//	GenerateContent(ctx, *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error]
package fakellm

import (
	"context"
	"fmt"
	"iter"
	"sync"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// Turn is one scripted model response.
//
// Exactly one of Text, Call or Err should be set. A Turn with a Call makes the
// agent invoke a tool; a Turn with Text ends the loop with a final answer.
type Turn struct {
	// Text is a plain-text model reply.
	Text string
	// Call makes the model emit a function call.
	Call *genai.FunctionCall
	// Err makes GenerateContent yield an error, for testing transient-failure
	// handling.
	Err error
}

// TextTurn is a convenience constructor for a final text answer.
func TextTurn(text string) Turn { return Turn{Text: text} }

// CallTurn is a convenience constructor for a tool call.
func CallTurn(name string, args map[string]any) Turn {
	return Turn{Call: &genai.FunctionCall{Name: name, Args: args}}
}

// Model is a scripted model.LLM.
//
// Turns are consumed in order, one per GenerateContent call. Exhausting the
// script is a test failure mode, not a silent zero value: further calls yield
// ErrScriptExhausted so a runaway agent loop is loud.
//
// Model is safe for concurrent use; parallel critic nodes (week 6) call it from
// several goroutines at once.
type Model struct {
	name string

	mu       sync.Mutex
	turns    []Turn
	position int
	requests []*model.LLMRequest
}

// ErrScriptExhausted reports that the agent asked for more turns than the test
// scripted.
var ErrScriptExhausted = fmt.Errorf("fakellm: script exhausted")

// New returns a Model that replays turns in order.
func New(name string, turns ...Turn) *Model {
	return &Model{name: name, turns: turns}
}

// Name implements model.LLM.
func (m *Model) Name() string { return m.name }

// GenerateContent implements model.LLM.
//
// The bool parameter is ADK's stream flag; a scripted model yields the same
// single response either way, which is what makes streaming and non-streaming
// assertions comparable in a lab.
func (m *Model) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	var turn Turn
	exhausted := m.position >= len(m.turns)
	if !exhausted {
		turn = m.turns[m.position]
		m.position++
	}
	m.mu.Unlock()

	return func(yield func(*model.LLMResponse, error) bool) {
		if exhausted {
			yield(nil, ErrScriptExhausted)
			return
		}
		if turn.Err != nil {
			yield(nil, turn.Err)
			return
		}
		part := &genai.Part{Text: turn.Text}
		if turn.Call != nil {
			part = &genai.Part{FunctionCall: turn.Call}
		}
		yield(&model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}},
		}, nil)
	}
}

// Requests returns a copy of every LLMRequest the agent issued, in order.
//
// This is the assertion surface for "what did the agent actually send the
// model" — the question the week 2 lecture's event log was meant to answer.
func (m *Model) Requests() []*model.LLMRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.LLMRequest, len(m.requests))
	copy(out, m.requests)
	return out
}

// CallCount reports how many times the agent called the model.
func (m *Model) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

// Remaining reports how many scripted turns were never consumed. A non-zero
// value at the end of a test usually means the agent stopped earlier than the
// test author expected.
func (m *Model) Remaining() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.turns) - m.position
}
