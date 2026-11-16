package main

// HITL approval before the final report (HW10).
//
// In human mode the gate pauses the graph with ResumeOrRequestInput and shows
// evidence, not prose: the report hash, the critics' verdicts and the cited
// sources. The decision lives in a ledger; the resume message only wakes the
// gate up, it is not trusted as a decision. In eval mode (the self-improvement
// loop scores dozens of reports) the gate approves as "system:eval" — and says
// so in the ledger: the eval does not pretend a human looked.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
)

// Decision statuses.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusDenied   = "denied"
	StatusTimedOut = "timed_out"
	StatusInvalid  = "invalid"
)

// GateMode selects who decides.
type GateMode int

const (
	// GateAuto approves without a pause (eval runs).
	GateAuto GateMode = iota
	// GateHuman pauses the graph and waits for a ledger decision.
	GateHuman
)

// Record is one approval request in the ledger.
type Record struct {
	ID            string    `json:"interrupt_id"`
	Question      string    `json:"question"`
	ReportHash    string    `json:"report_hash"`
	CriticsPassed bool      `json:"critics_passed"`
	Status        string    `json:"status"`
	By            string    `json:"by,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	AskedAt       time.Time `json:"asked_at"`
	DecidedAt     time.Time `json:"decided_at,omitzero"`
}

// Ledger stores approval requests and decisions; every change is appended
// to an io.Writer as a JSON line (approvals.log). Safe for concurrent use.
type Ledger struct {
	mu   sync.Mutex
	recs map[string]Record
	w    io.Writer
	now  func() time.Time
}

// ErrAlreadyDecided: a request is decided once; a late answer changes nothing.
var ErrAlreadyDecided = errors.New("рішення вже прийнято")

// NewLedger creates a ledger; w may be nil (no file).
func NewLedger(w io.Writer) *Ledger {
	return &Ledger{recs: map[string]Record{}, w: w, now: time.Now}
}

// Ask records a pending request. Idempotent: the re-run after resume keeps
// the first record.
func (l *Ledger) Ask(r Record) Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	if old, ok := l.recs[r.ID]; ok {
		return old
	}
	r.Status, r.AskedAt = StatusPending, l.now()
	l.recs[r.ID] = r
	l.write(r)
	return r
}

// Decide records a decision for a pending request, once.
func (l *Ledger) Decide(id, status, by, reason string) (Record, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.recs[id]
	if !ok {
		return Record{}, fmt.Errorf("ledger: немає запиту %s", id)
	}
	if r.Status != StatusPending {
		return r, fmt.Errorf("%w: %s → %s", ErrAlreadyDecided, id, r.Status)
	}
	r.Status, r.By, r.Reason, r.DecidedAt = status, by, reason, l.now()
	l.recs[id] = r
	l.write(r)
	return r, nil
}

// Get returns a request.
func (l *Ledger) Get(id string) (Record, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.recs[id]
	return r, ok
}

func (l *Ledger) write(r Record) {
	if l.w == nil {
		return
	}
	if data, err := json.Marshal(r); err == nil {
		_, _ = l.w.Write(append(data, '\n'))
	}
}

// Gate builds the approval node.
type Gate struct {
	Mode   GateMode
	Ledger *Ledger
}

// GateResult is the gate's output.
type GateResult struct {
	InterruptID string `json:"interrupt_id"`
	Status      string `json:"status"`
	By          string `json:"by"`
	Reason      string `json:"reason"`
	Draft       Draft  `json:"draft"`
}

// Evidence shown to the human: facts about the report, not the prose itself.
func gateEvidence(d Draft) map[string]any {
	return map[string]any{
		"question":       d.Question,
		"report":         d.Report,
		"report_hash":    reportHash(d.Report),
		"critics_passed": d.CriticsPassed,
		"critic_rounds":  d.Rounds,
		"verdicts":       verdictLine(d.Verdicts),
		"cited_sources":  citedIDs(d.Report),
		"evidence_from":  d.Evidence.Sources,
	}
}

func reportHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:8])
}

// Node returns the approval_gate node.
func (g *Gate) Node() workflow.Node {
	rerun := true
	return workflow.NewEmittingFunctionNode("approval_gate",
		func(ctx agent.Context, d Draft, emit func(*session.Event) error) (GateResult, error) {
			id := "approve-report-" + ctx.InvocationID()
			g.Ledger.Ask(Record{ID: id, Question: d.Question, ReportHash: reportHash(d.Report), CriticsPassed: d.CriticsPassed})
			if g.Mode == GateAuto {
				rec, err := g.Ledger.Decide(id, StatusApproved, "system:eval",
					"автоматичне затвердження в режимі eval: людина не моделюється")
				if err != nil && !errors.Is(err, ErrAlreadyDecided) {
					return GateResult{}, err
				}
				return g.result(ctx, emit, rec, d)
			}
			msg := fmt.Sprintf("Затвердити звіт %s? Критики: %s.", reportHash(d.Report), passedWord(d.CriticsPassed))
			if _, err := workflow.ResumeOrRequestInput(ctx, emit, session.RequestInput{
				InterruptID: id, Message: msg, Payload: gateEvidence(d),
			}); err != nil {
				return GateResult{}, err
			}
			rec, _ := g.Ledger.Get(id)
			if rec.Status == StatusPending {
				var err error
				rec, err = g.Ledger.Decide(id, StatusInvalid, "system:gate",
					"у журналі немає рішення; відповідь в обхід журналу не є рішенням")
				if err != nil && !errors.Is(err, ErrAlreadyDecided) {
					return GateResult{}, err
				}
			}
			return g.result(ctx, emit, rec, d)
		},
		workflow.NodeConfig{RerunOnResume: &rerun},
	)
}

func (g *Gate) result(ctx agent.Context, emit func(*session.Event) error, rec Record, d Draft) (GateResult, error) {
	r := GateResult{InterruptID: rec.ID, Status: rec.Status, By: rec.By, Reason: rec.Reason, Draft: d}
	return r, emit(trace(ctx, "approval_gate", fmt.Sprintf("%s by %s; критики: %s", rec.Status, rec.By, passedWord(d.CriticsPassed))))
}

func passedWord(ok bool) string {
	if ok {
		return "пройшли"
	}
	return "НЕ пройшли"
}

// finalText is what the user gets. Only an approved report is released.
func finalText(g GateResult) string {
	if g.Status != StatusApproved {
		return fmt.Sprintf("ЗВІТ НЕ ВИПУЩЕНО (%s, %s): %s", g.Status, g.By, g.Reason)
	}
	var b strings.Builder
	b.WriteString(g.Draft.Report)
	fmt.Fprintf(&b, "\n\n— затверджено: %s; критики: %s (%d раунд.); %s",
		g.By, passedWord(g.Draft.CriticsPassed), g.Draft.Rounds, reportHash(g.Draft.Report))
	return b.String()
}

// ParseDecision reads one reply word. Anything else is not a decision.
func ParseDecision(word string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "approve", "approved", "yes", "y", "так":
		return StatusApproved, nil
	case "deny", "denied", "reject", "no", "n", "ні":
		return StatusDenied, nil
	}
	return StatusInvalid, fmt.Errorf("нерозпізнана відповідь %q", word)
}
