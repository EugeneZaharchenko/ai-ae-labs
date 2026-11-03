package main

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"sync"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// The offline stand-ins: a rule-based worker and a scripted proposer.
//
// They are two different kinds of fake, and the labels say which. The WORKER
// reads its own transcript and decides what to do next, as a model does — so a
// strategy that gives it kb_read and tells it to open documents really does
// change what it answers, and the metric really does move. The PROPOSER is a
// fixed script of five candidates, chosen to exercise every outcome the loop
// can reach; it proves the brakes and the wiring and is NOT evidence that an LLM
// can improve a strategy. Neither reports token usage, because fabricated
// counts would make the bill a lie.

// workerStandIn answers questions through the same tools a model would get.
type workerStandIn struct{}

func (workerStandIn) Name() string { return "offline-worker (rule-based, NOT an LLM)" }

func (workerStandIn) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		question, reply := currentTurn(req.Contents)
		var resp *model.LLMResponse
		switch {
		case reply == nil:
			resp = call("c1", ToolSearch, map[string]any{"query": question})
		case reply.Name == ToolSearch:
			var sr SearchResult
			recode(reply.Response, &sr)
			// The strategy decides whether the worker may, and is told to, open a
			// document. Both must hold: a tool it does not have cannot be called,
			// and an instruction that does not mention it does not make it a habit.
			if len(sr.Hits) > 0 && hasTool(req, ToolRead) && mentions(req, ToolRead) {
				resp = call("c2", ToolRead, map[string]any{"id": sr.Hits[0].ID})
			} else {
				var parts []string
				for _, h := range sr.Hits {
					parts = append(parts, h.Title+": "+h.Snippet)
				}
				resp = say("From the search results only — " + strings.Join(parts, " "))
			}
		case reply.Name == ToolRead:
			var rr ReadResult
			recode(reply.Response, &rr)
			resp = say("According to the policy: " + rr.Text)
		default:
			resp = say("I could not find this in the policy documents.")
		}
		yield(resp, nil)
	}
}

// scriptedProposer plays five candidates in order. Each exists to exercise one
// outcome of the loop.
type scriptedProposer struct {
	mu sync.Mutex
	n  int
}

func (*scriptedProposer) Name() string { return "scripted-proposer (NOT an LLM)" }

func (p *scriptedProposer) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		p.mu.Lock()
		c := proposerScript[p.n%len(proposerScript)]
		p.n++
		p.mu.Unlock()
		yield(say("```\n"+c+"```"), nil)
	}
}

var proposerScript = []string{
	// 1. The real improvement: give the worker kb_read and tell it to use it.
	"# Open the best document before answering\ntools: kb_search, kb_read\n\nSearch, then open the best document with kb_read before answering (the search result is only its scope sentence) and quote every figure you find.\n",
	// 2. An escape attempt: the same good strategy, aimed outside the surface.
	"path: ../escape.md\n# Open the best document before answering\ntools: kb_search, kb_read\n\nSearch, then open the best document with kb_read before answering.\n",
	// 3. A tool that does not exist.
	"# Run a shell command\ntools: kb_search, shell\n\nSearch, then run shell commands to find the figures.\n",
	// 4. A regression: take kb_read away again.
	"# Answer briefly\ntools: kb_search\n\nAnswer in one short sentence.\n",
	// 5. A rewording of the winner: same behaviour, so no gain.
	"# Open every relevant document\ntools: kb_search, kb_read\n\nSearch, open the best document with kb_read, and quote every figure you find in your answer.\n",
}

func call(id, name string, args map[string]any) *model.LLMResponse {
	return &model.LLMResponse{
		Content:      genai.NewContentFromParts([]*genai.Part{{FunctionCall: &genai.FunctionCall{ID: id, Name: name, Args: args}}}, genai.RoleModel),
		FinishReason: genai.FinishReasonStop,
	}
}

func say(s string) *model.LLMResponse {
	return &model.LLMResponse{Content: genai.NewContentFromText(s, genai.RoleModel), FinishReason: genai.FinishReasonStop}
}

// currentTurn returns the user's question and the latest tool reply after it.
func currentTurn(contents []*genai.Content) (string, *genai.FunctionResponse) {
	var reply *genai.FunctionResponse
	for i := len(contents) - 1; i >= 0; i-- {
		for j := len(contents[i].Parts) - 1; j >= 0; j-- {
			p := contents[i].Parts[j]
			if p.FunctionResponse != nil && reply == nil {
				reply = p.FunctionResponse
			}
			if contents[i].Role == genai.RoleUser && p.FunctionResponse == nil && p.Text != "" {
				return p.Text, reply
			}
		}
	}
	return "", reply
}

func hasTool(req *model.LLMRequest, name string) bool {
	_, ok := req.Tools[name]
	return ok
}

// mentions reports whether the system instruction names the tool.
func mentions(req *model.LLMRequest, name string) bool {
	if req.Config == nil || req.Config.SystemInstruction == nil {
		return false
	}
	for _, p := range req.Config.SystemInstruction.Parts {
		if strings.Contains(p.Text, name) {
			return true
		}
	}
	return false
}

// recode moves a tool reply into a typed struct through JSON, which is how it
// arrives whether or not the provider round-tripped it.
func recode(from map[string]any, to any) {
	b, err := json.Marshal(from)
	if err != nil {
		panic(fmt.Sprintf("demo: %v", err))
	}
	_ = json.Unmarshal(b, to)
}
