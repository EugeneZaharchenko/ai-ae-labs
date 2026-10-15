// Package graphmemory is long-term agent memory backed by a durable graph store.
//
// It has two seams of deliberately different size:
//
//   - Consumers see ADK's own memory.Service (AddSessionToMemory, SearchMemory),
//     plus the recall operations SearchNodes and OpenNodes, which return the
//     knowledge-graph shape of the go-sdk reference memory MCP server
//     (Entity / Relation / KnowledgeGraph).
//   - A database implements Store: two methods, Upsert and Search.
//
// Ingestion is implicit: a whole session goes in, every text event becomes one
// Turn keyed by its event ID, so adding the same session again is a no-op. That
// property matters because ingestion runs inside a Temporal Activity, which may
// be retried.
package graphmemory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// DefaultLimit is how many turns a Query returns when Limit is zero.
const DefaultLimit = 20

// MaxLimit caps every Query, so a recall tool can never hand the model an
// unbounded blob.
const MaxLimit = 50

// Turn is one remembered utterance, with where it came from.
type Turn struct {
	ID        string    `json:"id"` // the session event ID — the idempotency key
	AppName   string    `json:"app"`
	UserID    string    `json:"user"`
	SessionID string    `json:"session_id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	At        time.Time `json:"at"`
	Entities  []string  `json:"entities,omitempty"`
}

// Query selects turns of one user of one app.
//
// Text and Entities are both optional. When both are set, a turn must match
// both. Text matches word-wise and case-insensitively: a turn matches when it
// contains any word of Text.
type Query struct {
	AppName  string
	UserID   string
	Text     string
	Entities []string
	Since    time.Time // zero means no lower bound
	Limit    int       // 0 means DefaultLimit; capped at MaxLimit
}

// Store is one durable database.
//
// Upsert must be idempotent by Turn.ID. Search returns matching turns newest
// first, never nil.
type Store interface {
	Upsert(ctx context.Context, turns []Turn) error
	Search(ctx context.Context, q Query) ([]Turn, error)
}

// ErrScope reports a query without the app or user that scopes it.
var ErrScope = errors.New("graphmemory: query needs AppName and UserID")

// Service adapts a Store to ADK memory.Service and to the recall operations.
type Service struct {
	store Store
}

var _ memory.Service = (*Service)(nil)

// New returns a Service over s.
func New(s Store) *Service {
	return &Service{store: s}
}

// AddSessionToMemory stores every text event of s as a Turn.
//
// Events without text (tool calls, tool responses, state deltas) are skipped:
// memory holds what was said, not the mechanics of saying it.
func (m *Service) AddSessionToMemory(ctx context.Context, s session.Session) error {
	turns := TurnsFromSession(s)
	if len(turns) == 0 {
		return nil
	}
	if err := m.store.Upsert(ctx, turns); err != nil {
		return fmt.Errorf("graphmemory: add session %s: %w", s.ID(), err)
	}
	return nil
}

// SearchMemory implements memory.Service with a keyword search.
func (m *Service) SearchMemory(ctx context.Context, req *memory.SearchRequest) (*memory.SearchResponse, error) {
	turns, err := m.Search(ctx, Query{AppName: req.AppName, UserID: req.UserID, Text: req.Query})
	if err != nil {
		return nil, err
	}
	res := &memory.SearchResponse{Memories: make([]memory.Entry, 0, len(turns))}
	for _, t := range turns {
		res.Memories = append(res.Memories, memory.Entry{
			ID:             t.ID,
			Content:        genai.NewContentFromText(t.Text, genai.Role(roleOf(t.Author))),
			Author:         t.Author,
			Timestamp:      t.At,
			CustomMetadata: map[string]any{"session_id": t.SessionID, "entities": t.Entities},
		})
	}
	return res, nil
}

// Search validates and normalises q, then asks the store.
func (m *Service) Search(ctx context.Context, q Query) ([]Turn, error) {
	q, err := Normalize(q)
	if err != nil {
		return nil, err
	}
	turns, err := m.store.Search(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("graphmemory: search: %w", err)
	}
	if turns == nil {
		turns = []Turn{}
	}
	return turns, nil
}

// Normalize applies the Query defaults every backend relies on.
func Normalize(q Query) (Query, error) {
	if q.AppName == "" || q.UserID == "" {
		return q, ErrScope
	}
	switch {
	case q.Limit <= 0:
		q.Limit = DefaultLimit
	case q.Limit > MaxLimit:
		q.Limit = MaxLimit
	}
	q.Text = strings.TrimSpace(q.Text)
	ents := make([]string, 0, len(q.Entities))
	for _, e := range q.Entities {
		if e = CanonicalEntity(e); e != "" && !slices.Contains(ents, e) {
			ents = append(ents, e)
		}
	}
	q.Entities = ents
	return q, nil
}

// TurnsFromSession converts the text events of s into turns.
func TurnsFromSession(s session.Session) []Turn {
	var turns []Turn
	for ev := range s.Events().All() {
		text := eventText(ev)
		if ev == nil || ev.ID == "" || text == "" {
			continue
		}
		turns = append(turns, Turn{
			ID:        ev.ID,
			AppName:   s.AppName(),
			UserID:    s.UserID(),
			SessionID: s.ID(),
			Author:    ev.Author,
			Text:      text,
			At:        ev.Timestamp.UTC(),
			Entities:  ExtractEntities(text),
		})
	}
	return turns
}

func eventText(ev *session.Event) string {
	if ev == nil || ev.Content == nil {
		return ""
	}
	var parts []string
	for _, p := range ev.Content.Parts {
		if p != nil && !p.Thought && strings.TrimSpace(p.Text) != "" {
			parts = append(parts, strings.TrimSpace(p.Text))
		}
	}
	return strings.Join(parts, " ")
}

func roleOf(author string) string {
	if author == "user" {
		return genai.RoleUser
	}
	return genai.RoleModel
}
