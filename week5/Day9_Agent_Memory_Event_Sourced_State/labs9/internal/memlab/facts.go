// Package memlab is the plumbing of the Day 9 lab: everything a memory layer
// needs that is not "wire it into ADK".
//
// The assignment is the wiring — the remember_fact tool, the callbacks, the
// runner config, the audit export. This package holds the decisions those
// pieces call: which fact may overwrite which (facts.go), what is safe to
// ingest into long-term memory (store.go), what the model sees on a call
// (context.go), and how a session's event stream becomes an audit log
// (audit.go).
//
// Verified against google.golang.org/adk/v2 v2.6.0 (10/2026).
package memlab

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// FactPrefix is the state-key prefix of a durable fact. "user:" is the ADK
// scope that follows the user across sessions of one app; "fact." namespaces
// our records inside it so the context assembler can find them with one scan.
const FactPrefix = "user:fact."

// TaintKey is set (in temp: scope, so it never reaches the stored session) by
// any tool whose output came from outside LEDGERWORKS. While it is set,
// remember_fact writes at TierUntrusted and is refused.
const TaintKey = "temp:untrusted_input"

// Tier is how far a writer is trusted. The writer is decided by the program,
// never by the model: a model-supplied "source" argument is exactly the field
// an injected instruction would fill in.
type Tier int

const (
	TierUntrusted Tier = iota + 1 // retrieved partner pages, tickets, e-mails
	TierTool                      // first-party tool output (ledger lookup)
	TierUser                      // the authenticated user's own words
)

func (t Tier) String() string {
	switch t {
	case TierUntrusted:
		return "untrusted"
	case TierTool:
		return "tool"
	case TierUser:
		return "user"
	}
	return fmt.Sprintf("tier(%d)", int(t))
}

// Version is a superseded value. Keeping it is what makes a conflict an
// auditable event instead of a silent overwrite (Zep/Graphiti keep the same
// valid-from / invalid-at pair on every edge).
type Version struct {
	Value        string    `json:"value"`
	Tier         Tier      `json:"tier"`
	ObservedAt   time.Time `json:"observed_at"`
	SupersededAt time.Time `json:"superseded_at"`
}

// Fact is one durable statement about the user, as stored in session state
// under FactPrefix+key. It round-trips through JSON because that is how the
// database session service stores state.
type Fact struct {
	Value      string    `json:"value"`
	Tier       Tier      `json:"tier"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
	Previous   []Version `json:"previous,omitempty"`
}

// ToState converts the fact to a JSON-shaped value for StateDelta.
func (f Fact) ToState() (map[string]any, error) {
	b, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("memlab: encode fact: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("memlab: encode fact: %w", err)
	}
	return m, nil
}

// FactFromState decodes whatever the session service returned for a fact key.
func FactFromState(v any) (Fact, bool) {
	b, err := json.Marshal(v)
	if err != nil {
		return Fact{}, false
	}
	var f Fact
	if json.Unmarshal(b, &f) != nil || f.Value == "" {
		return Fact{}, false
	}
	return f, true
}

// Verdict is the outcome of Policy.Decide.
type Verdict string

const (
	Accept    Verdict = "accepted"   // new key
	Unchanged Verdict = "unchanged"  // same value again — refresh nothing
	Supersede Verdict = "superseded" // new value replaced an older one
	Reject    Verdict = "rejected"
)

// Decision is what remember_fact must do and tell the model.
type Decision struct {
	Verdict Verdict
	Reason  string
	// Fact is the record to write; zero value when Verdict is Reject/Unchanged.
	Fact Fact
}

// Policy is the memory write policy: which keys may exist, how long each stays
// trustworthy, and a clock. TTL 0 means the fact never goes stale.
type Policy struct {
	// Keys maps an allowed fact key to its freshness window.
	Keys map[string]time.Duration
	// MaxValueRunes bounds a value; a "fact" that is a paragraph is a document.
	MaxValueRunes int
	// MaxHistory bounds Previous so one flapping key cannot grow the row.
	MaxHistory int
	// Now is injectable so tests do not sleep.
	Now func() time.Time
}

// DefaultPolicy is the LEDGERWORKS merchant-support policy.
func DefaultPolicy() Policy {
	return Policy{
		Keys: map[string]time.Duration{
			"integration":      90 * 24 * time.Hour, // legacy | new-api — re-confirm quarterly
			"recommended_path": 30 * 24 * time.Hour,
			"contact_channel":  180 * 24 * time.Hour,
			"do_not_suggest":   0,
		},
		MaxValueRunes: 120,
		MaxHistory:    5,
		Now:           time.Now,
	}
}

func (p Policy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Fresh reports whether f is still inside the key's window.
func (p Policy) Fresh(key string, f Fact) bool {
	base, _, _ := strings.Cut(key, ".")
	ttl := p.Keys[base]
	return ttl == 0 || p.now().Sub(f.ObservedAt) <= ttl
}

// Decide is the whole conflict and poisoning policy in one pure function.
//
// Order matters: shape and PII first (cheap, absolute), then trust, then the
// comparison with what is already stored.
func (p Policy) Decide(current *Fact, key, value string, tier Tier, source string) Decision {
	reject := func(format string, a ...any) Decision {
		return Decision{Verdict: Reject, Reason: fmt.Sprintf(format, a...)}
	}
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return reject("порожнє значення")
	case utf8.RuneCountInString(value) > p.MaxValueRunes:
		return reject("значення довше за %d символів — це документ, а не факт", p.MaxValueRunes)
	case ContainsPII(value):
		return reject("значення містить персональні дані — у пам'ять вони не потрапляють")
	}
	if !p.allowed(key) {
		return reject("ключ %q не дозволений політикою пам'яті", key)
	}
	if tier <= TierUntrusted {
		return reject("запис із недовіреного джерела (%s) заборонено — факт лишається пропозицією для людини", source)
	}

	now := p.now()
	next := Fact{Value: value, Tier: tier, Source: source, ObservedAt: now}
	if current == nil {
		return Decision{Verdict: Accept, Fact: next}
	}
	if current.Value == value {
		return Decision{Verdict: Unchanged, Reason: "те саме значення"}
	}
	if tier < current.Tier && p.Fresh(key, *current) {
		return reject("нижчий рівень довіри (%s) не може переписати свіжий факт рівня %s", tier, current.Tier)
	}
	hist := append([]Version{{
		Value: current.Value, Tier: current.Tier, ObservedAt: current.ObservedAt, SupersededAt: now,
	}}, current.Previous...)
	if p.MaxHistory > 0 && len(hist) > p.MaxHistory {
		hist = hist[:p.MaxHistory]
	}
	next.Previous = hist
	return Decision{Verdict: Supersede, Reason: fmt.Sprintf("замінено %q", current.Value), Fact: next}
}

var merchantSuffix = regexp.MustCompile(`^[A-Z]-\d{2,}$`)

// allowed accepts "<base>" or "<base>.<merchant-id>": the family is fixed by
// policy, the merchant scope is validated by shape, so a model cannot mint a
// free-form key such as "system_prompt".
func (p Policy) allowed(key string) bool {
	base, scope, scoped := strings.Cut(key, ".")
	if _, ok := p.Keys[base]; !ok {
		return false
	}
	return !scoped || merchantSuffix.MatchString(scope)
}
