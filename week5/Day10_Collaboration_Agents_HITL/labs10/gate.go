package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/week5/Day10_Collaboration_Agents_HITL/labs10/internal/approvals"
)

const actionSend = "send_board_pack"

// Evidence is what the reviewer sees: facts, not the coordinator's prose.
type Evidence struct {
	Action     string          `json:"action"`
	Recipients []string        `json:"recipients"`
	Artifact   string          `json:"artifact"`
	Hash       string          `json:"hash"`
	Diff       []string        `json:"diff"`
	RiskFacts  []string        `json:"risk_facts"`
	Sections   []SectionStatus `json:"sections"`
	Sources    []string        `json:"sources"`
	Gaps       []string        `json:"gaps"`
	// UnnamedGaps — gaps that the coordinator's summary did not mention.
	UnnamedGaps    []string `json:"unnamed_gaps,omitempty"`
	ApproveAllowed bool     `json:"approve_allowed"`
}

// SectionStatus is one line of the evidence per desk.
type SectionStatus struct {
	Desk   string `json:"desk"`
	Status string `json:"status"`
	Facts  int    `json:"facts"`
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ErrNoEvidence — nothing verified, so there is nothing to ask about.
var ErrNoEvidence = errors.New("запит на схвалення без конкретного доказу")

// buildEvidence turns a draft into the facts for the request.
func buildEvidence(d Draft) (Evidence, error) {
	ev := Evidence{
		Action: actionSend, Recipients: d.Recipients, Artifact: d.URI,
		Hash: d.Hash, Diff: d.Diff, UnnamedGaps: d.UnnamedGaps,
	}
	seen := map[string]bool{}
	var failures []string
	for _, s := range d.Sections {
		st := SectionStatus{Desk: s.Desk, Status: "verified", Facts: len(s.Findings)}
		if !s.Verified {
			st.Status, st.Code, st.Reason = "unverified", s.Code, s.Reason
			ev.Gaps = append(ev.Gaps, s.Desk)
			failures = append(failures, fmt.Sprintf("%s: %s %s", s.Desk, s.Code, s.Reason))
		}
		ev.Sections = append(ev.Sections, st)
		for _, f := range s.Findings {
			if s.Desk == riskDesk {
				ev.RiskFacts = append(ev.RiskFacts, fmt.Sprintf("%s (%s)", f.Claim, f.Source))
			}
			if !seen[f.Source] {
				seen[f.Source] = true
				ev.Sources = append(ev.Sources, f.Source)
			}
		}
	}
	if len(ev.Gaps) == len(d.Sections) {
		return ev, fmt.Errorf("%w: документ без жодного розділу; %s", ErrNoEvidence, strings.Join(failures, "; "))
	}
	ev.ApproveAllowed = len(ev.Gaps) == 0
	return ev, nil
}

// asMap is the ledger form of the evidence (approvals.Request.Facts).
func (ev Evidence) asMap() map[string]any {
	raw, _ := json.Marshal(ev)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

func evidenceOf(rec approvals.Record) (Evidence, error) {
	var ev Evidence
	if err := json.Unmarshal([]byte(rec.Facts), &ev); err != nil {
		return ev, fmt.Errorf("доказ запиту %s не читається: %w", rec.InterruptID, err)
	}
	return ev, nil
}

func interruptID(ctx agent.Context) string { return "board-pack-" + ctx.InvocationID() }

// newApprovalGateNode asks, pauses, and on resume reads the decision from the
// ledger. The reply that resumed the run is not trusted as a decision.
func newApprovalGateNode(e *Engine) workflow.Node {
	rerun := true
	return workflow.NewEmittingFunctionNode("approval_gate",
		func(ctx agent.Context, d Draft, emit func(*session.Event) error) (GateResult, error) {
			ev, err := buildEvidence(d)
			if err != nil {
				return GateResult{}, err
			}
			id := interruptID(ctx)
			// Ask is idempotent: the re-run after resume keeps the first deadline.
			if _, err := e.Store.Ask(ctx, approvals.Request{
				InterruptID: id, SessionID: ctx.SessionID(), Action: actionSend,
				ArtifactURI: d.URI, Hash: d.Hash, Recipients: d.Recipients,
				Facts: ev.asMap(), Window: e.Window,
			}); err != nil {
				return GateResult{}, fmt.Errorf("approval_gate: %w", err)
			}
			reply, err := workflow.ResumeOrRequestInput(ctx, emit, session.RequestInput{
				InterruptID: id,
				Message:     fmt.Sprintf("Надіслати %s для %v? Хеш %s.", d.URI, d.Recipients, d.Hash),
				Payload:     ev.asMap(),
			})
			if err != nil {
				return GateResult{}, err
			}
			rec, err := e.Store.Get(ctx, id)
			if err != nil {
				return GateResult{}, fmt.Errorf("approval_gate: %w", err)
			}
			if rec.Status == approvals.Pending {
				rec, err = e.Store.Decide(ctx, id, approvals.Decision{
					Status: approvals.Invalid, By: "system:gate",
					Reason: fmt.Sprintf("у журналі немає рішення; відповідь %q в обхід журналу не є рішенням", fmt.Sprint(reply)),
				})
				if err != nil && !errors.Is(err, approvals.ErrAlreadyDecided) {
					return GateResult{}, fmt.Errorf("approval_gate: %w", err)
				}
			}
			return GateResult{InterruptID: id, Status: rec.Status, By: rec.DecidedBy, Reason: rec.Reason, Draft: d}, nil
		},
		workflow.NodeConfig{RerunOnResume: &rerun},
	)
}

// Reasons a wait ends without a human decision. Each one is its own record.
var (
	ErrDecisionTimeout   = errors.New("вікно рішення минуло")
	ErrDecisionCancelled = errors.New("очікування рішення скасовано")
	ErrRepliesClosed     = errors.New("канал відповідей закрито")
	ErrUnrecognizedReply = errors.New("нерозпізнана відповідь")
)

// parseVerdict reads one reply word. Anything else is not a decision.
func parseVerdict(word string) (approvals.Status, error) {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "approve", "approved", "yes", "так":
		return approvals.Approved, nil
	case "deny", "denied", "reject", "no", "ні":
		return approvals.Denied, nil
	}
	return approvals.Invalid, fmt.Errorf("%w: %q", ErrUnrecognizedReply, word)
}

// awaitDecision waits for one reply inside the window. Every path that is not
// a clear yes ends as a refusal; the error names which path it was.
func awaitDecision(ctx context.Context, replies <-chan string, window time.Duration, by string) (approvals.Decision, error) {
	timer := time.NewTimer(window)
	defer timer.Stop()
	select {
	case <-timer.C:
		return approvals.Decision{Status: approvals.TimedOut, By: "system:timeout",
			Reason: fmt.Sprintf("ніхто не відповів за %s", window)}, ErrDecisionTimeout
	case <-ctx.Done():
		return approvals.Decision{Status: approvals.TimedOut, By: "system:cancel",
			Reason: "очікування перервано: " + ctx.Err().Error()}, fmt.Errorf("%w: %w", ErrDecisionCancelled, ctx.Err())
	case word, ok := <-replies:
		if !ok {
			return approvals.Decision{Status: approvals.TimedOut, By: "system:ui",
				Reason: "канал відповідей закрився до рішення"}, ErrRepliesClosed
		}
		status, err := parseVerdict(word)
		if err != nil {
			return approvals.Decision{Status: approvals.Invalid, By: by,
				Reason: fmt.Sprintf("нерозпізнана відповідь %q", word)}, err
		}
		reason := "людина схвалила"
		if status == approvals.Denied {
			reason = "людина відмовила"
		}
		return approvals.Decision{Status: status, By: by, Reason: reason}, nil
	}
}
