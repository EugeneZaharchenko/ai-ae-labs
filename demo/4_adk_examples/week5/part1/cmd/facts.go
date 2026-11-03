package main

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// The pure layer: what a fact is, who may write one, and how a stream of
// StateDelta events folds back into state. Nothing here imports ADK, so every
// rule the demo depends on is tested without a runner, a model or a session.
//
// Names match the labs' Day 9 internal/memlab (FactPrefix, TaintKey, the
// remember_fact key vocabulary) so the demo and the graded lab are one
// vocabulary rather than two.

const (
	// FactPrefix puts a fact in USER state. ADK files a "user:" key under
	// (app, user) rather than under the session, so a fact written in Monday's
	// session is visible in Tuesday's — that scope, not a database, is what
	// makes it long-lived. An unprefixed key would die with its session.
	FactPrefix = "user:fact."

	// TaintKey marks an invocation that has read foreign text. "temp:" keys
	// live for one invocation and are never persisted (session.AppendEvent
	// trims them), which is exactly the lifetime a taint mark should have.
	TaintKey = "temp:untrusted_input"
)

// families is the allowlist of fact keys. A key outside it cannot exist, no
// matter how the request is phrased — "is_admin" and "payout_limit" are absent
// on purpose. The prompt says WHEN to remember; this says WHAT may be remembered.
var families = []string{"integration", "recommended_path", "contact_channel", "do_not_suggest"}

var merchantRe = regexp.MustCompile(`^[A-Z]-\d{2,}$`)

const maxValueRunes = 80

// FactInput is what the model sends to remember_fact. The json and jsonschema
// tags become the declaration the provider sees.
type FactInput struct {
	Key   string `json:"key" jsonschema:"Fact key: <family>.<merchant id>, e.g. integration.A-114. Families: integration, recommended_path, contact_channel, do_not_suggest"`
	Value string `json:"value" jsonschema:"Short fact value, e.g. legacy or new-api"`
}

// FactOutput is what the model reads back. ADK hands it over as a flat map of
// these json fields, so the offline stand-in reads reply["status"] directly.
type FactOutput struct {
	Status string `json:"status"` // accepted | unchanged | rejected
	Key    string `json:"key"`
	Reason string `json:"reason,omitempty"`
}

// Validate checks the shape of a request. It does not decide whether the
// writer is trusted — that is Decide's job, and keeping the two apart is what
// lets a test say which of them refused a write.
func (in FactInput) Validate() error {
	family, merchant, ok := strings.Cut(strings.TrimSpace(in.Key), ".")
	switch {
	case !ok:
		return fmt.Errorf("key %q is not <family>.<merchant id>", in.Key)
	case !slices.Contains(families, family):
		return fmt.Errorf("family %q is not one this agent may remember (allowed: %s)", family, strings.Join(families, ", "))
	case !merchantRe.MatchString(merchant):
		return fmt.Errorf("%q is not a merchant id (they look like A-114)", merchant)
	}
	v := strings.TrimSpace(in.Value)
	switch {
	case v == "":
		return fmt.Errorf("value is empty")
	case strings.EqualFold(v, "undefined"), strings.EqualFold(v, "null"):
		return fmt.Errorf("%q is a placeholder, not a fact", v)
	case len([]rune(v)) > maxValueRunes:
		return fmt.Errorf("value is %d characters; a fact is a short value, not a paragraph", len([]rune(v)))
	}
	return nil
}

// Decide is the write policy, as a function of everything it needs and nothing
// else: the request, the current value, and whether this invocation read
// foreign text. guard=false is the policy the incident ran under.
func Decide(in FactInput, current string, has, tainted, guard bool) FactOutput {
	key := strings.TrimSpace(in.Key)
	if err := in.Validate(); err != nil {
		return FactOutput{Status: "rejected", Key: key, Reason: err.Error()}
	}
	if guard && tainted {
		return FactOutput{Status: "rejected", Key: key,
			Reason: "this turn read foreign text (fetch_partner_doc); a fact can only change on the user's word"}
	}
	if has && current == strings.TrimSpace(in.Value) {
		return FactOutput{Status: "unchanged", Key: key}
	}
	return FactOutput{Status: "accepted", Key: key}
}

// Fold replays StateDelta maps in order and returns the state they produce.
//
// It is deliberately the same rule session.AppendEvent applies — a flat
// maps.Copy, last writer wins — and NOT a deep merge: a map-valued key is
// replaced whole. temp: keys are dropped because they are never persisted. If
// Fold over the stored events does not reproduce the stored state, the event
// log is not a sufficient record and "event-sourced" was a word, not a property.
func Fold(deltas []map[string]any) map[string]any {
	state := map[string]any{}
	for _, d := range deltas {
		for k, v := range d {
			if strings.HasPrefix(k, "temp:") {
				continue
			}
			state[k] = v
		}
	}
	return state
}

// UserFacts picks the fact keys out of a state map and strips the prefix.
func UserFacts(state map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range state {
		if name, ok := strings.CutPrefix(k, FactPrefix); ok {
			out[name] = fmt.Sprint(v)
		}
	}
	return out
}

// SameUserState reports whether two states agree on every user: key.
func SameUserState(a, b map[string]any) bool {
	pick := func(m map[string]any) map[string]string {
		out := map[string]string{}
		for k, v := range m {
			if strings.HasPrefix(k, "user:") {
				out[k] = fmt.Sprint(v)
			}
		}
		return out
	}
	return maps.Equal(pick(a), pick(b))
}

// RenderFacts is the block the agent sees in its instruction on every turn.
// Sorted, so the same state produces the same prompt.
func RenderFacts(facts map[string]string) string {
	if len(facts) == 0 {
		return "Known facts about this user: none yet."
	}
	names := make([]string, 0, len(facts))
	for k := range facts {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("Known facts about this user (durable user: state):")
	for _, k := range names {
		fmt.Fprintf(&b, "\n- %s = %s", k, facts[k])
	}
	return b.String()
}

// Row is one line of the audit, derived from the event stream. There is no
// second log to keep in sync with the first.
type Row struct {
	Session string
	Author  string
	Kind    string // state_delta | rejected
	Key     string
	Detail  string
}

func (r Row) String() string {
	return fmt.Sprintf("[ev:%s] %s %s [%s, %s]", r.Kind, r.Key, r.Detail, r.Session, r.Author)
}
