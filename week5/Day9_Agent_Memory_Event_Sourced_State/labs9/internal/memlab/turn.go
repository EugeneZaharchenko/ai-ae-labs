package memlab

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/compaction"
	"google.golang.org/genai"
)

// Turn is everything one runner.Run produced.
type Turn struct {
	Text   string   // last non-empty model text
	Tools  []string // tool calls, in order
	Events []*session.Event
	// CompactionErr is set when compaction failed. It is bookkeeping, not the
	// user's answer: the turn still succeeded, so Say does not return it as err.
	CompactionErr error
}

// Say sends one user message on (user, sid) and drains the event stream.
// The session is created on first use (runner.Config.AutoCreateSession).
func Say(ctx context.Context, r *runner.Runner, user, sid, text string) (Turn, error) {
	var t Turn
	msg := genai.NewContentFromText(text, genai.RoleUser)
	for e, err := range r.Run(ctx, user, sid, msg, agent.RunConfig{}) {
		if err != nil {
			if errors.Is(err, compaction.ErrCompaction) {
				t.CompactionErr = err
				continue
			}
			return t, fmt.Errorf("memlab: run: %w", err)
		}
		t.Events = append(t.Events, e)
		if e.LLMResponse.Content == nil || e.LLMResponse.Partial {
			continue
		}
		var b strings.Builder
		for _, p := range e.LLMResponse.Content.Parts {
			if p == nil {
				continue
			}
			if p.FunctionCall != nil {
				t.Tools = append(t.Tools, p.FunctionCall.Name)
			}
			if !p.Thought {
				b.WriteString(p.Text)
			}
		}
		if s := strings.TrimSpace(b.String()); s != "" && e.Author != "user" {
			t.Text = s
		}
	}
	return t, nil
}

// StoredEvents returns every event the session service has for a session —
// the persisted record, which (unlike Turn.Events) includes the compaction
// summaries: tail retention appends its summary directly, so it is never
// yielded on the stream.
func StoredEvents(ctx context.Context, svc session.Service, app, user, sid string) ([]*session.Event, error) {
	got, err := svc.Get(ctx, &session.GetRequest{AppName: app, UserID: user, SessionID: sid})
	if err != nil {
		return nil, fmt.Errorf("memlab: get session: %w", err)
	}
	var out []*session.Event
	for e := range got.Session.Events().All() {
		out = append(out, e)
	}
	return out, nil
}
