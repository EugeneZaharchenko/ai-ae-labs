package main

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/contrib/googleadk"
	"google.golang.org/adk/v2/session"

	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/ledger"
)

// Activities is everything that touches the disk or the network. The workflow
// only calls them; it never does I/O itself, because on replay a line of
// workflow code runs again, while a finished Activity does not.
type Activities struct {
	Desk     *ledger.Desk
	Sessions session.Service // SQLite (.data/agent.db) in the worker
	Memory   *graphmemory.Service
}

// SessionKey names one conversation.
type SessionKey struct {
	AppName   string
	UserID    string
	SessionID string
}

// OpenRefundCase is the side-effecting call. Its errors are typed for the
// RetryPolicy: TransientError is retried, SemanticError and FatalError are not.
func (a *Activities) OpenRefundCase(ctx context.Context, in ledger.RefundCaseInput) (ledger.RefundCaseOutput, error) {
	out, err := a.Desk.OpenRefundCase(ctx, in)
	return out, toActivityError(err)
}

// GetMerchantPayouts is read-only.
func (a *Activities) GetMerchantPayouts(ctx context.Context, in ledger.PayoutsInput) (ledger.PayoutsOutput, error) {
	out, err := ledger.GetMerchantPayouts(ctx, in)
	return out, toActivityError(err)
}

// LoadSession reads the stored conversation; nil means a new session.
func (a *Activities) LoadSession(ctx context.Context, key SessionKey) (*googleadk.SessionSnapshot, error) {
	snap, err := googleadk.ExportSession(ctx, a.Sessions, key.AppName, key.UserID, key.SessionID)
	if errors.Is(err, session.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, toActivityError(fmt.Errorf("load session: %w", &ledger.TransientError{Op: "load_session", Reason: err.Error()}))
	}
	return snap, nil
}

// PersistSession writes the new events of the turn into SQLite. Events
// already stored (by ID) are skipped, so a retried Activity does not write
// anything twice.
func (a *Activities) PersistSession(ctx context.Context, snap *googleadk.SessionSnapshot) error {
	resp, err := a.Sessions.Get(ctx, &session.GetRequest{AppName: snap.AppName, UserID: snap.UserID, SessionID: snap.SessionID})
	if errors.Is(err, session.ErrNotFound) {
		if _, err := googleadk.ImportSession(ctx, a.Sessions, snap); err != nil {
			return toActivityError(&ledger.TransientError{Op: "persist_session", Reason: err.Error()})
		}
		return nil
	}
	if err != nil {
		return toActivityError(&ledger.TransientError{Op: "persist_session", Reason: err.Error()})
	}
	stored := map[string]bool{}
	for ev := range resp.Session.Events().All() {
		stored[ev.ID] = true
	}
	for _, ev := range snap.Events {
		if ev == nil || stored[ev.ID] {
			continue
		}
		if err := a.Sessions.AppendEvent(ctx, resp.Session, ev); err != nil {
			return toActivityError(&ledger.TransientError{Op: "persist_session", Reason: err.Error()})
		}
	}
	return nil
}

// RememberSession is the only write path into long-term memory. Turns are
// keyed by event ID, so a retry does not add anything twice.
func (a *Activities) RememberSession(ctx context.Context, snap *googleadk.SessionSnapshot) error {
	tmp := session.InMemoryService()
	s, err := googleadk.ImportSession(ctx, tmp, snap)
	if err != nil {
		return toActivityError(fmt.Errorf("remember session: %w", err))
	}
	if err := a.Memory.AddSessionToMemory(ctx, s); err != nil {
		return toActivityError(&ledger.TransientError{Op: "remember_session", Reason: err.Error()})
	}
	return nil
}

// SearchNodesInput / OpenNodesInput carry the scope from the workflow; the
// model only gives the query or the names.
type SearchNodesInput struct {
	AppName, UserID, Query string
}

type OpenNodesInput struct {
	AppName, UserID string
	Names           []string
}

// SearchNodes finds remembered turns by words.
func (a *Activities) SearchNodes(ctx context.Context, in SearchNodesInput) (graphmemory.KnowledgeGraph, error) {
	g, err := a.Memory.SearchNodes(ctx, in.AppName, in.UserID, in.Query, 0)
	return g, memoryError(err)
}

// OpenNodes returns everything remembered about known ids, e.g. A-114.
func (a *Activities) OpenNodes(ctx context.Context, in OpenNodesInput) (graphmemory.KnowledgeGraph, error) {
	g, err := a.Memory.OpenNodes(ctx, in.AppName, in.UserID, in.Names, 0)
	return g, memoryError(err)
}

func memoryError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, graphmemory.ErrScope) {
		return toActivityError(err) // a bug in our scope, not worth a retry
	}
	return toActivityError(&ledger.TransientError{Op: "memory", Reason: err.Error()})
}
