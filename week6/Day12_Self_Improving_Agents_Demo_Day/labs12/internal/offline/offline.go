// Package offline is the stand-in worker model the Day 12 lab falls back to
// when no LLM provider is configured.
//
// It is NOT a language model and the lab says so every time it is used. It is
// a few lines of rules that read the strategy's instruction text and behave
// accordingly, so that the whole loop — propose, apply, score, keep or revert,
// audit — can be run, tested and demonstrated with no key and no network.
// What it proves is the plumbing and the brakes. It cannot prove that a real
// model improves its own strategy; only a run against a real provider can
// (LAB_LIVE=1, see Homework.md).
//
// Three phrases in the instruction switch behaviour on, and they are the same
// three levers a real model reacts to:
//
//	"kb_read"                 read the top-ranked document instead of answering from snippets
//	"every relevant document" read the top three documents, not one
//	"every figure"            quote every sentence containing a figure, not a summary
package offline

import (
	"context"
	"iter"
	"strings"
	"unicode"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// Name is what the lab prints as the model, so a transcript can never be
// mistaken for a real run.
const Name = "offline-rules (NOT an LLM)"

// Worker is the rule-based research worker.
type Worker struct{}

// Name implements model.LLM.
func (Worker) Name() string { return Name }

// GenerateContent implements model.LLM.
func (Worker) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: next(req)}, nil)
	}
}

func next(req *model.LLMRequest) *genai.Content {
	instr := strings.ToLower(systemText(req))
	question := firstUserText(req.Contents)
	tools := declared(req)

	var hits []map[string]any
	var bodies []string
	searched := false
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if p == nil || p.FunctionResponse == nil {
				continue
			}
			switch p.FunctionResponse.Name {
			case "kb_search":
				if !searched {
					searched = true
					hits = asHits(p.FunctionResponse.Response["hits"])
				}
			case "kb_read":
				if b, ok := p.FunctionResponse.Response["body"].(string); ok {
					bodies = append(bodies, b)
				}
			}
		}
	}

	if !searched {
		return call("kb_search", map[string]any{"query": question})
	}

	want := 0
	if strings.Contains(instr, "kb_read") && tools["kb_read"] {
		want = 1
		if strings.Contains(instr, "every relevant document") {
			want = 3
		}
	}
	if len(bodies) < want && len(bodies) < len(hits) {
		id, _ := hits[len(bodies)]["id"].(string)
		return call("kb_read", map[string]any{"id": id})
	}

	// Answer from what was read, or from the snippets if nothing was.
	var source []string
	source = append(source, bodies...)
	if len(source) == 0 {
		for _, h := range hits {
			s, _ := h["snippet"].(string)
			source = append(source, s)
		}
	}
	text := strings.Join(source, " ")
	if strings.Contains(instr, "every figure") {
		text = figureSentences(text)
	} else {
		text = firstSentence(text)
	}
	if strings.TrimSpace(text) == "" {
		text = "No matching policy found."
	}
	return &genai.Content{Role: "model", Parts: []*genai.Part{{Text: text}}}
}

func call(name string, args map[string]any) *genai.Content {
	return &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: name, Args: args}}}}
}

func systemText(req *model.LLMRequest) string {
	if req.Config == nil || req.Config.SystemInstruction == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range req.Config.SystemInstruction.Parts {
		if p != nil {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func firstUserText(cs []*genai.Content) string {
	for _, c := range cs {
		if c.Role != "user" {
			continue
		}
		for _, p := range c.Parts {
			if p != nil && p.Text != "" {
				return p.Text
			}
		}
	}
	return ""
}

func declared(req *model.LLMRequest) map[string]bool {
	out := map[string]bool{}
	if req.Config == nil {
		return out
	}
	for _, t := range req.Config.Tools {
		if t == nil {
			continue
		}
		for _, d := range t.FunctionDeclarations {
			out[d.Name] = true
		}
	}
	return out
}

func asHits(v any) []map[string]any {
	list, _ := v.([]any)
	var out []map[string]any
	for _, x := range list {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func sentences(s string) []string {
	var out []string
	for _, part := range strings.SplitAfter(s, ". ") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func firstSentence(s string) string {
	ss := sentences(s)
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}

func figureSentences(s string) string {
	var keep []string
	for _, sent := range sentences(s) {
		if strings.IndexFunc(sent, unicode.IsDigit) >= 0 {
			keep = append(keep, sent)
		}
	}
	return strings.Join(keep, " ")
}
