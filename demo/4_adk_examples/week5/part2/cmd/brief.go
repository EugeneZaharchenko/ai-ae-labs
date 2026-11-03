package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// The pure layer: the plan, the sections the desks return, how they merge into
// one brief, and what a human decision looks like. No ADK here, so the rules a
// board brief must satisfy are tested without a runner.
//
// Names follow the labs' Day 10 solution (collaborative_workflow_engine, the
// *_desk nodes, gather, approval_gate, dispatch) so the demo and the graded lab
// read as one system.

// desks is the catalog, in the order sections appear in the brief. Order comes
// from here and never from a map, so the same run renders the same document —
// and the same document hashes the same.
var desks = []string{"risk_desk", "compliance_desk", "integration_desk"}

// facts is what each desk may cite. A desk answers from its sheet or says it
// cannot; it does not research. Grounding is what keeps three parallel model
// calls from producing three confident, mutually inconsistent stories.
var facts = map[string]string{
	"risk_desk": `- Cutover of the payout pipeline needs a settlement-free window; the shortest observed is 4h (Sunday 02:00-06:00).
- Rollback has been rehearsed once, in staging only.
- 37 of 400 merchants still send payouts through the legacy API.`,
	"compliance_desk": `- Moving to the new processor changes the PCI DSS cardholder-data scope; a re-scoping assessment is required before go-live.
- NBU reporting must continue without a gap across the cutover day.
- GDPR data residency does not change: both processors are in the EU.`,
	"integration_desk": `- Merchant A-114 is on the legacy integration; their own migration is scheduled for Q4.
- 37 merchants on the legacy API, 12 of them with custom payout file formats.
- The new API has no equivalent of the legacy batch-correction endpoint yet.`,
}

// Plan is the Plan phase's output: the task and the desks it fans out to.
type Plan struct {
	Task  string   `json:"task"`
	Desks []string `json:"desks"`
}

// Section is one desk's contribution. A desk that failed returns a Section
// with Gap set instead of failing the graph: one broken specialist must not
// take the other two down with it, and the gap is named in the brief.
type Section struct {
	Desk string `json:"desk"`
	Text string `json:"text,omitempty"`
	Gap  string `json:"gap,omitempty"`
}

// Brief is what the human approves: the rendered document and its hash.
type Brief struct {
	Text  string   `json:"text"`
	Hash  string   `json:"hash"`
	Gaps  []string `json:"gaps,omitempty"`
	Count int      `json:"count"` // sections with content
}

// Merge orders the sections by the catalog and renders the brief. It reads
// only Section values out of the joined map — the join also carries the plan,
// keyed "plan", which is how the merge knows which desks were asked.
func Merge(plan Plan, joined map[string]any) Brief {
	var b strings.Builder
	fmt.Fprintf(&b, "BOARD BRIEF — %s\n", plan.Task)
	var gaps []string
	count := 0
	for _, d := range plan.Desks {
		s, ok := joined[d].(Section)
		switch {
		case !ok:
			gaps = append(gaps, d+": no section arrived")
			fmt.Fprintf(&b, "\n## %s\nGAP: no section arrived\n", d)
		case s.Gap != "":
			gaps = append(gaps, d+": "+s.Gap)
			fmt.Fprintf(&b, "\n## %s\nGAP: %s\n", d, s.Gap)
		default:
			count++
			fmt.Fprintf(&b, "\n## %s\n%s\n", d, strings.TrimSpace(s.Text))
		}
	}
	text := b.String()
	return Brief{Text: text, Hash: Hash(text), Gaps: gaps, Count: count}
}

// Hash binds an approval to one exact document. An approval of "the brief"
// that does not say which bytes it approved is an approval of whatever the
// brief becomes after it.
func Hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:12]
}

// Decision is the human's reply, decoded from the resume payload.
type Decision struct {
	Approved bool   `json:"approved"`
	By       string `json:"by"`
	Note     string `json:"note,omitempty"`
	Hash     string `json:"hash"` // the document the human was shown
}

// DecodeDecision reads the reply ADK hands back from ResumedInput. ADK parses
// a JSON string in {"response": …} into a map, so that is the shape here.
// Anything else is not a decision, and "not a decision" must never read as yes.
func DecodeDecision(v any) (Decision, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return Decision{}, fmt.Errorf("approval reply is %T, not a decision", v)
	}
	d := Decision{}
	d.Approved, _ = m["approved"].(bool)
	d.By, _ = m["by"].(string)
	d.Note, _ = m["note"].(string)
	d.Hash, _ = m["hash"].(string)
	if strings.TrimSpace(d.By) == "" {
		return Decision{}, fmt.Errorf("approval reply names no approver")
	}
	return d, nil
}

// Outcome is what the gate passes to dispatch.
type Outcome struct {
	Brief    Brief    `json:"brief"`
	Decision Decision `json:"decision"`
}

// Sendable is the one rule dispatch enforces: an approved decision, for this
// exact document. A yes for a different hash is a yes for a different brief.
func (o Outcome) Sendable() error {
	switch {
	case !o.Decision.Approved:
		return fmt.Errorf("rejected by %s: %s", o.Decision.By, o.Decision.Note)
	case o.Decision.Hash != o.Brief.Hash:
		return fmt.Errorf("approval is for document %s, not %s", o.Decision.Hash, o.Brief.Hash)
	}
	return nil
}
