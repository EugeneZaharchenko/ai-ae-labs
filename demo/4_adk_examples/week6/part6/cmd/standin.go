package main

import (
	"context"
	"encoding/json"
	"iter"
	"regexp"
	"strconv"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// standIn is the offline model: it reads its own transcript and picks the next
// step with plain rules. It is not an LLM and says so in its name — it lets the
// graph, the tools, the surface guard and the A2A wire run with no provider.
type standIn struct{}

func (standIn) Name() string { return "offline-stand-in (NOT an LLM)" }

var reURL = regexp.MustCompile(`https?://\S+`)
var reAmount = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(?:EUR|eur|€)`)

func (standIn) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: next(req.Contents), TurnComplete: true}, nil)
	}
}

// next answers with a tool call for a fresh user turn, and with the tool's
// result once it has come back.
func next(contents []*genai.Content) *genai.Content {
	if len(contents) == 0 {
		return say("Ask me about a refund amount, an egress check, or a ledger deletion.")
	}
	last := contents[len(contents)-1]
	for _, p := range last.Parts {
		if p.FunctionResponse != nil {
			b, _ := json.Marshal(p.FunctionResponse.Response)
			return say(p.FunctionResponse.Name + " → " + string(b))
		}
	}
	var text string
	for _, p := range last.Parts {
		text += p.Text
	}
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "delete"):
		return call("delete_ledger", map[string]any{"order_id": "8841"})
	case strings.Contains(low, "egress") || strings.Contains(low, "network") || reURL.MatchString(text):
		u := reURL.FindString(text)
		if u == "" {
			u = "https://example.com"
		}
		return call("check_egress", map[string]any{"url": u})
	case reAmount.MatchString(text):
		v, _ := strconv.ParseFloat(reAmount.FindStringSubmatch(text)[1], 64)
		return call("refund_policy", map[string]any{"amount_eur": v})
	}
	return say("Ask me about a refund amount (e.g. 120 EUR), an egress check, or a ledger deletion.")
}

func say(s string) *genai.Content {
	return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: s}}}
}

func call(name string, args map[string]any) *genai.Content {
	return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: name + "-1", Name: name, Args: args}}}}
}
