package ledgerkb

import (
	"strings"
	"unicode"
)

// Fact is one thing a correct answer must contain. Any of the Forms counts;
// they are alternative spellings of the same figure, not alternative facts.
type Fact struct {
	Name  string
	Forms []string
}

// Case is one eval question with its golden facts.
type Case struct {
	ID       string
	Question string
	Facts    []Fact
}

func f(name string, forms ...string) Fact { return Fact{Name: name, Forms: forms} }

// Dev is the set the loop optimizes against: the keep/discard decision is
// made on it and the proposer sees how it did. 8 questions, 25 facts.
func Dev() []Case {
	return []Case{
		{"D1", "A merchant asks for a 3,200 EUR refund on a transaction from 100 days ago. What approval and timing apply?", []Fact{
			f("second approval", "compliance officer"), f("processing time", "5 business days", "five business days"), f("window", "120 days")}},
		{"D2", "By when must merchant evidence for a chargeback be submitted, what fee applies, and which team handles reason code 4837?", []Fact{
			f("evidence deadline", "7 calendar days", "seven calendar days"), f("fee", "15 eur"), f("team", "fraud team")}},
		{"D3", "When does a standard payout arrive for a Growth-tariff merchant, what is the cut-off, and what does an instant payout cost?", []Fact{
			f("schedule", "t+2"), f("cut-off", "14:00"), f("instant fee", "1.0%", "1%")}},
		{"D4", "What must LEDGERWORKS evidence for the cardholder-data audit?", []Fact{
			f("level", "level 1"), f("tokenization", "tokenvault", "token"), f("scans", "asv")}},
		{"D5", "A new merchant expects 700,000 EUR monthly volume. Which KYC applies, from what ownership share are owners identified, and how often is a high-risk merchant reviewed?", []Fact{
			f("edd", "enhanced due diligence"), f("ubo", "25%"), f("high-risk cycle", "6 months", "six months")}},
		{"D6", "An incident is classified SEV1. What are the paging time, the update cadence and the post-mortem deadline?", []Fact{
			f("page", "5 minutes", "five minutes"), f("cadence", "30 minutes", "every 30"), f("post-mortem", "5 business days", "five business days")}},
		{"D7", "How long do we keep transaction records, call recordings and application logs?", []Fact{
			f("transactions", "7 years", "seven years"), f("recordings", "90 days"), f("logs", "30 days")}},
		{"D8", "Can an AI agent approve an 800 EUR refund by itself, and who reviews a change to an agent's strategy file within what time?", []Fact{
			f("limit", "500 eur"), f("reviewer", "compliance"), f("review time", "2 business days", "two business days")}},
	}
}

// Heldout is never shown to the proposer and never used for keep/discard. It
// is scored before and after a run: a gain on Dev that does not show up here
// is overfitting, not improvement.
func Heldout() []Case {
	return []Case{
		{"H1", "What is the FX margin, at what time are rates fixed, and which settlement currency is reported to the regulator?", []Fact{
			f("margin", "0.8%"), f("fixing", "10:00"), f("currency", "uah")}},
		{"H2", "What first-response time does a P1 ticket get, and after how many SLA breaches in a month is the account escalated?", []Fact{
			f("p1", "15 minutes"), f("coverage", "24x7"), f("escalation", "two sla breaches", "2 sla breaches", "two breaches", "2 breaches")}},
		{"H3", "How large is the rolling reserve for a high-risk merchant, for how long, and what is the Standard tariff?", []Fact{
			f("reserve", "5%"), f("duration", "180 days"), f("standard", "2.9%")}},
		{"H4", "How long are KYC files kept after offboarding, and how often is a medium-risk merchant reviewed?", []Fact{
			f("kyc", "5 years", "five years"), f("medium", "12 months", "twelve months")}},
	}
}

// Normalize lowercases and unifies the characters that differ only in
// typography, so "5 business days" in a model's answer matches regardless of
// non-breaking spaces or dash variants.
func Normalize(s string) string {
	r := strings.NewReplacer(" ", " ", " ", " ", "–", "-", "—", "-", "‑", "-")
	return strings.Join(strings.Fields(strings.ToLower(r.Replace(s))), " ")
}

// Score counts how many of facts occur in answer. It is a pure function: no
// model, no I/O, the same inputs always give the same number — which is what
// makes it usable as a fitness metric.
func Score(answer string, facts []Fact) (found, total int) {
	a := Normalize(answer)
	for _, fact := range facts {
		for _, form := range fact.Forms {
			if strings.Contains(a, Normalize(form)) {
				found++
				break
			}
		}
	}
	return found, len(facts)
}

// Missing names the facts absent from answer. Used for audit detail only; the
// proposer is never shown it (see Leaks).
func Missing(answer string, facts []Fact) []string {
	a := Normalize(answer)
	var out []string
	for _, fact := range facts {
		ok := false
		for _, form := range fact.Forms {
			if strings.Contains(a, Normalize(form)) {
				ok = true
				break
			}
		}
		if !ok {
			out = append(out, fact.Name)
		}
	}
	return out
}

// Leaks returns the golden forms of cases that appear verbatim in text. A
// strategy that contains them is not a better strategy, it is a cheat sheet;
// the engine refuses such a candidate before it is ever scored.
func Leaks(text string, cases []Case) []string {
	t := Normalize(text)
	seen := map[string]bool{}
	var out []string
	for _, c := range cases {
		for _, fact := range c.Facts {
			for _, form := range fact.Forms {
				nf := Normalize(form)
				if leakable(nf) && strings.Contains(t, nf) && !seen[nf] {
					seen[nf] = true
					out = append(out, form)
				}
			}
		}
	}
	return out
}

// leakable says whether a golden form is specific enough to count as a leak:
// a figure ("15 eur", "t+2") or a multi-word phrase ("fraud team"). Single
// plain words like "token" are vocabulary a legitimate strategy may use.
func leakable(form string) bool {
	if strings.ContainsRune(form, ' ') {
		return true
	}
	return len(form) >= 3 && strings.IndexFunc(form, unicode.IsDigit) >= 0
}

// TotalFacts sums the facts over cases.
func TotalFacts(cases []Case) int {
	n := 0
	for _, c := range cases {
		n += len(c.Facts)
	}
	return n
}
