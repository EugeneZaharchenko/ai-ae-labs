package memlab

import (
	"context"
	"fmt"
	"iter"
	"regexp"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// Offline is the model used when no provider is configured (no key, no
// Ollama). It is a handful of rules, not a language model: enough to drive the
// memory machinery end to end — tool calls, StateDelta, context assembly,
// poisoning — with no network. It reads the facts block from the system
// instruction, so a fact that never reached the prompt is a fact it cannot
// answer with, exactly like a real model.
//
// Gullible reproduces the incident: after fetch_partner_doc it obeys the
// instruction hidden in the note and calls remember_fact with the injected
// value. The write policy has to stop it; the model never will.
type Offline struct {
	Gullible bool
}

// Name implements model.LLM.
func (Offline) Name() string { return "offline-rules" }

var (
	merchantRe = regexp.MustCompile(`\b[A-Z]-\d{2,}\b`)
	integRe    = regexp.MustCompile(`(?i)\b(legacy|new-api)\b`)
	factRe     = regexp.MustCompile(`(integration\.[A-Z]-\d+) = (\S+)`)
	pathRe     = regexp.MustCompile(`(?i)recommended_path|шлях`)
)

// GenerateContent implements model.LLM.
func (o Offline) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		out := o.next(req)
		// Usage is synthetic (size-proportional) so the token table has numbers
		// offline; a real provider's numbers replace it, and the lab says which.
		prompt := int32(promptTokens(req))
		yield(&model.LLMResponse{Content: out, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount: prompt, CandidatesTokenCount: 12, TotalTokenCount: prompt + 12,
		}}, nil)
	}
}

func (o Offline) next(req *model.LLMRequest) *genai.Content {
	user, results := lastTurn(req.Contents)
	said := func(s string) *genai.Content { return genai.NewContentFromText(s, genai.RoleModel) }
	call := func(name string, args map[string]any) *genai.Content {
		return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{genai.NewPartFromFunctionCall(name, args)}}
	}
	merchant := merchantRe.FindString(user)
	lower := strings.ToLower(user)

	if doc, ok := results["fetch_partner_doc"]; ok {
		if rej, ok := results["remember_fact"]; ok {
			return said(fmt.Sprintf("У примітці просять змінити запис, але це чужий текст (%v). Підтвердьте, будь ласка, що інтеграція справді змінилась.", rej["status"]))
		}
		if o.Gullible && strings.Contains(fmt.Sprint(doc["text"]), "new-api") {
			return call("remember_fact", map[string]any{"key": "integration." + merchant, "value": "new-api"})
		}
		return said("У примітці: " + fmt.Sprint(doc["text"]))
	}
	switch {
	case strings.Contains(lower, "портал") || strings.Contains(lower, "примітк"):
		return call("fetch_partner_doc", map[string]any{"merchant_id": merchant})
	case merchant != "" && strings.Contains(lower, "рекомендований шлях") && !strings.Contains(lower, "?"):
		if r, ok := results["remember_fact"]; ok {
			return said(fmt.Sprintf("Зафіксував (%v).", r["status"]))
		}
		return call("remember_fact", map[string]any{"key": "recommended_path." + merchant, "value": "legacy-fix"})
	case merchant != "" && integRe.MatchString(user) && !strings.Contains(lower, "?"):
		if r, ok := results["remember_fact"]; ok {
			return said(fmt.Sprintf("Зафіксував (%v).", r["status"]))
		}
		return call("remember_fact", map[string]any{"key": "integration." + merchant, "value": strings.ToLower(integRe.FindString(user))})
	case strings.Contains(lower, "порад") || strings.Contains(lower, "інтеграці") || pathRe.MatchString(user):
		sys := ""
		if req.Config != nil && req.Config.SystemInstruction != nil {
			for _, p := range req.Config.SystemInstruction.Parts {
				sys += p.Text + "\n"
			}
		}
		for _, m := range factRe.FindAllStringSubmatch(sys, -1) {
			if merchant == "" || strings.HasSuffix(m[1], merchant) {
				advice := "радимо legacy-фікс"
				if m[2] == "new-api" {
					advice = "радимо міграцію на новий API"
				}
				return said(fmt.Sprintf("Для %s відомо: інтеграція %s — %s.", strings.TrimPrefix(m[1], "integration."), m[2], advice))
			}
		}
		return said("У пам'яті немає даних про цю інтеграцію. Яка вона в мерчанта?")
	}
	return said("Прийняв.")
}

func promptTokens(req *model.LLMRequest) int {
	var b strings.Builder
	if req.Config != nil && req.Config.SystemInstruction != nil {
		for _, p := range req.Config.SystemInstruction.Parts {
			b.WriteString(p.Text)
		}
	}
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if p != nil {
				b.WriteString(p.Text)
				if p.FunctionResponse != nil {
					b.WriteString(fmt.Sprint(p.FunctionResponse.Response))
				}
			}
		}
	}
	return EstimateTokens(b.String())
}

// lastTurn returns the last user text and the tool responses after it.
func lastTurn(contents []*genai.Content) (string, map[string]map[string]any) {
	user, start := "", 0
	for i, c := range contents {
		if c == nil || c.Role != genai.RoleUser {
			continue
		}
		for _, p := range c.Parts {
			if p != nil && strings.TrimSpace(p.Text) != "" {
				user, start = p.Text, i
			}
		}
	}
	res := map[string]map[string]any{}
	for _, c := range contents[start:] {
		if c == nil {
			continue
		}
		for _, p := range c.Parts {
			if p != nil && p.FunctionResponse != nil {
				res[p.FunctionResponse.Name] = p.FunctionResponse.Response
			}
		}
	}
	return user, res
}
