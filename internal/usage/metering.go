package usage

import (
	"context"
	"iter"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// Metering wraps a model.LLM so that every call's usage is counted.
//
// Week 4 part 1 fed the Meter by hand: the loop made the model call itself, so
// it held the response with the token counts in it and could add them. From part
// 2 on, an llmagent makes that call — inside the framework — and nothing hands
// the response back to us. So the counting moves to where the calls pass
// through: a decorator with the same interface, wrapped around whatever
// internal/modelcfg built.
//
// Both fields are required. It is deliberately the whole of what this decorator
// does: a model wrapper is also where caching, retries and routing get bolted
// on, this course has a module for each of those, and a wrapper that quietly did
// four things would make the bill impossible to attribute.
type Metering struct {
	Inner model.LLM
	Meter *Meter
}

// Name implements model.LLM by delegating. The wrapped model keeps its own
// identity on purpose: the provider is selected by name, so a decorator that
// renamed itself would send the request to a model that does not exist.
func (m Metering) Name() string { return m.Inner.Name() }

// GenerateContent implements model.LLM, passing the sequence through untouched.
func (m Metering) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		// A streaming provider sends the turn as many partial chunks and then the
		// finished one, and it may report the running totals on every chunk or
		// only on the partials. So the usage is held until the completed response
		// arrives: counting per chunk would bill one call three or four times, and
		// counting only the final chunk would miss a provider that puts usage
		// nowhere else. Week 4 part 1's hand-written loop hoists usage the same way.
		var latest *genai.GenerateContentResponseUsageMetadata
		for resp, err := range m.Inner.GenerateContent(ctx, req, stream) {
			if resp != nil && resp.UsageMetadata != nil {
				latest = resp.UsageMetadata
			}
			if err == nil && resp != nil && !resp.Partial {
				u := resp.UsageMetadata
				if u == nil {
					u = latest
				}
				m.Meter.Add(u)
				latest = nil
			}
			if !yield(resp, err) {
				return
			}
		}
	}
}
