package memlab

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// EstimateTokens is a deliberately crude size estimate (Ukrainian text costs
// about one token per 2.5 characters on current tokenizers). It exists to
// budget the facts block BEFORE the call; what the call really cost comes
// back in UsageMetadata and is what Meter records.
func EstimateTokens(s string) int { return (len([]rune(s))*2 + 4) / 5 }

// CallRecord is one model call as the provider reported it.
type CallRecord struct {
	Model         string
	PromptTokens  int32 // UsageMetadata.PromptTokenCount
	OutputTokens  int32 // UsageMetadata.CandidatesTokenCount
	TotalTokens   int32 // UsageMetadata.TotalTokenCount
	FactsTokens   int   // our estimate of the facts block in this call
	FactsKept     int
	FactsDropped  int
	ReportedUsage bool // false when the provider returned no usage at all
}

// Meter accumulates per-call token accounting for one process. It is the
// answer to "what does memory cost per turn" — a number from the provider,
// not from a tokenizer guess.
type Meter struct {
	mu      sync.Mutex
	calls   []CallRecord
	pending CallRecord
}

// Calls returns a copy of every recorded call, oldest first.
func (m *Meter) Calls() []CallRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls)
}

// Totals sums the reported prompt/output tokens.
func (m *Meter) Totals() (prompt, output int64) {
	for _, c := range m.Calls() {
		prompt += int64(c.PromptTokens)
		output += int64(c.OutputTokens)
	}
	return prompt, output
}

func (m *Meter) note(est, kept, dropped int) {
	m.mu.Lock()
	m.pending = CallRecord{FactsTokens: est, FactsKept: kept, FactsDropped: dropped}
	m.mu.Unlock()
}

// AfterModel is an llmagent.AfterModelCallback that records UsageMetadata.
// It never alters the response.
func (m *Meter) AfterModel() llmagent.AfterModelCallback {
	return func(_ agent.Context, resp *model.LLMResponse, err error) (*model.LLMResponse, error) {
		if err != nil || resp == nil || resp.Partial {
			return nil, nil
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		rec := m.pending
		m.pending = CallRecord{}
		rec.Model = resp.ModelVersion
		if u := resp.UsageMetadata; u != nil {
			rec.PromptTokens, rec.OutputTokens, rec.TotalTokens = u.PromptTokenCount, u.CandidatesTokenCount, u.TotalTokenCount
			rec.ReportedUsage = true
		}
		m.calls = append(m.calls, rec)
		return nil, nil
	}
}

// ActiveFact is one decoded fact. Stale facts are returned with Stale=true so
// the caller can choose to show them flagged.
type ActiveFact struct {
	Key   string
	Fact  Fact
	Stale bool
}

// FactsFromState decodes all FactPrefix keys of a state, ordered by tier
// (high first), then most recent, then key — a stable order, so the same state
// always renders the same prompt (and therefore hits the same prompt cache).
func FactsFromState(st session.State, p Policy) []ActiveFact {
	var out []ActiveFact
	for k, v := range st.All() {
		key, ok := strings.CutPrefix(k, FactPrefix)
		if !ok {
			continue
		}
		if f, ok := FactFromState(v); ok {
			out = append(out, ActiveFact{Key: key, Fact: f, Stale: !p.Fresh(key, f)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Fact.Tier != b.Fact.Tier {
			return a.Fact.Tier > b.Fact.Tier
		}
		if !a.Fact.ObservedAt.Equal(b.Fact.ObservedAt) {
			return a.Fact.ObservedAt.After(b.Fact.ObservedAt)
		}
		return a.Key < b.Key
	})
	return out
}

// RenderFacts builds the system-instruction block within budget tokens and
// reports what it kept and dropped. Facts below TierTool never enter the
// prompt as instructions; stale facts enter flagged, because «I am not sure
// any more» is more useful to the model than silence.
func RenderFacts(facts []ActiveFact, budget int) (block string, kept, dropped int) {
	var b strings.Builder
	b.WriteString("Відомі факти про цього користувача (довготривала пам'ять; не вигадуй інших):\n")
	used := EstimateTokens(b.String())
	for _, f := range facts {
		if f.Fact.Tier < TierTool {
			dropped++
			continue
		}
		line := fmt.Sprintf("- %s = %s (джерело: %s, %s)", f.Key, f.Fact.Value, f.Fact.Tier, f.Fact.ObservedAt.Format("2006-01-02"))
		if f.Stale {
			line += " [ЗАСТАРІЛО — перепитай користувача, перш ніж спиратись]"
		}
		if cost := EstimateTokens(line); used+cost <= budget {
			b.WriteString(line + "\n")
			used += cost
			kept++
		} else {
			dropped++
		}
	}
	if kept == 0 {
		return "", 0, dropped
	}
	return b.String(), kept, dropped
}

// FactsContext is the context-assembly step of the call: a BeforeModelCallback
// that appends the durable facts to the system instruction. It is the point
// where "what is remembered" becomes "what the model sees", and the only
// place a fact may enter a prompt as an instruction.
func FactsContext(p Policy, budget int, meter *Meter) llmagent.BeforeModelCallback {
	return func(ctx agent.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
		block, kept, dropped := RenderFacts(FactsFromState(ctx.State(), p), budget)
		if meter != nil {
			meter.note(EstimateTokens(block), kept, dropped)
		}
		if block == "" {
			return nil, nil
		}
		if req.Config == nil {
			req.Config = &genai.GenerateContentConfig{}
		}
		req.Config.SystemInstruction = appendInstruction(req.Config.SystemInstruction, block)
		return nil, nil
	}
}

func appendInstruction(si *genai.Content, text string) *genai.Content {
	if si == nil {
		return genai.NewContentFromText(text, genai.RoleUser)
	}
	si.Parts = append(si.Parts, genai.NewPartFromText(text))
	return si
}

// TaintAfterTool marks the invocation as having read untrusted input once any
// of the named tools returns. The mark lives in temp: state — visible for the
// rest of this invocation, dropped before the session is stored — so the next
// user message starts clean.
func TaintAfterTool(names ...string) llmagent.AfterToolCallback {
	return func(ctx agent.Context, t tool.Tool, _, _ map[string]any, err error) (map[string]any, error) {
		if err == nil && slices.Contains(names, t.Name()) {
			if setErr := ctx.State().Set(TaintKey, true); setErr != nil {
				return nil, fmt.Errorf("memlab: taint: %w", setErr)
			}
		}
		return nil, nil
	}
}

// TierFor decides who is writing: tainted invocation → untrusted, otherwise
// the user. Never the model's own claim.
func TierFor(st session.State) Tier {
	if v, err := st.Get(TaintKey); err == nil {
		if b, _ := v.(bool); b {
			return TierUntrusted
		}
	}
	return TierUser
}
