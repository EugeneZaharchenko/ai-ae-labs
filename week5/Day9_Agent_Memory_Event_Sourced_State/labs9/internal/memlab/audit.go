package memlab

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"google.golang.org/adk/v2/session"
)

// AuditRow is one line of the audit log. Every row is derived from the session
// event stream — there is no second log to keep in sync with the first.
type AuditRow struct {
	Seq        int       `json:"seq"`
	At         time.Time `json:"at"`
	Invocation string    `json:"invocation"`
	Author     string    `json:"author"`
	Kind       string    `json:"kind"` // state_delta | rejected | compaction
	Key        string    `json:"key,omitempty"`
	Detail     string    `json:"detail,omitempty"`
}

// Audit folds a session's events into audit rows: what changed (StateDelta),
// what was refused (remember_fact answered "rejected") and when history was
// compacted. temp: keys never appear — they are not stored.
func Audit(events []*session.Event) []AuditRow {
	var rows []AuditRow
	add := func(e *session.Event, kind, key, detail string) {
		rows = append(rows, AuditRow{
			Seq: len(rows) + 1, At: e.Timestamp, Invocation: e.InvocationID,
			Author: e.Author, Kind: kind, Key: key, Detail: detail,
		})
	}
	for _, e := range events {
		keys := make([]string, 0, len(e.Actions.StateDelta))
		for k := range e.Actions.StateDelta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if strings.HasPrefix(k, "temp:") {
				continue
			}
			add(e, "state_delta", k, summarize(e.Actions.StateDelta[k]))
		}
		if e.Actions.Compaction != nil {
			add(e, "compaction", "", fmt.Sprintf("%d подій замінено підсумком", len(e.Actions.Compaction.ExcludedEvents)))
		}
		if e.LLMResponse.Content == nil {
			continue
		}
		for _, p := range e.LLMResponse.Content.Parts {
			if p == nil || p.FunctionResponse == nil || p.FunctionResponse.Name != "remember_fact" {
				continue
			}
			if p.FunctionResponse.Response["status"] == string(Reject) {
				add(e, "rejected", fmt.Sprint(p.FunctionResponse.Response["key"]), fmt.Sprint(p.FunctionResponse.Response["reason"]))
			}
		}
	}
	return rows
}

func summarize(v any) string {
	if f, ok := FactFromState(v); ok {
		s := fmt.Sprintf("%s [%s, %s]", f.Value, f.Tier, f.Source)
		if len(f.Previous) > 0 {
			s += fmt.Sprintf(" (було: %s)", f.Previous[0].Value)
		}
		return s
	}
	return fmt.Sprint(v)
}

// WriteAudit writes the rows as indented JSON — the artifact the assignment
// hands to the auditor.
func WriteAudit(path string, rows []AuditRow) error {
	b, err := AuditJSON(rows)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("memlab: write audit: %w", err)
	}
	return nil
}

// AuditJSON encodes rows as indented JSON with a trailing newline.
func AuditJSON(rows []AuditRow) ([]byte, error) {
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("memlab: encode audit: %w", err)
	}
	return append(b, '\n'), nil
}

// ReplayStateDeltas re-applies the persisted StateDelta of events to a fresh
// session through the same Service.AppendEvent every live write goes through,
// and returns the resulting state. If replay and original state differ, the
// event stream is NOT a sufficient record of the state — "event-sourced" was a
// word, not a property.
func ReplayStateDeltas(ctx context.Context, svc session.Service, app, user, sessionID string, events []*session.Event) (map[string]any, error) {
	created, err := svc.Create(ctx, &session.CreateRequest{AppName: app, UserID: user, SessionID: sessionID})
	if err != nil {
		return nil, fmt.Errorf("memlab: replay: create: %w", err)
	}
	for _, src := range events {
		if len(src.Actions.StateDelta) == 0 {
			continue
		}
		e := session.NewEvent(ctx, src.InvocationID)
		e.Author = src.Author
		e.Actions.StateDelta = src.Actions.StateDelta
		if err := svc.AppendEvent(ctx, created.Session, e); err != nil {
			return nil, fmt.Errorf("memlab: replay: append: %w", err)
		}
	}
	got, err := svc.Get(ctx, &session.GetRequest{AppName: app, UserID: user, SessionID: sessionID})
	if err != nil {
		return nil, fmt.Errorf("memlab: replay: get: %w", err)
	}
	out := map[string]any{}
	for k, v := range got.Session.State().All() {
		out[k] = v
	}
	return out, nil
}
