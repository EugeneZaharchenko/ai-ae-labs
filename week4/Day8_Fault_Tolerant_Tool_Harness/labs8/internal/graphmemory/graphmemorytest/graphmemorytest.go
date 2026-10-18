// Package graphmemorytest holds the conformance suite every graphmemory.Store
// must pass, and Fake, an in-process Store for tests.
//
// Fake is test support only. It is not durable and graphmemory/backend.Open
// cannot select it: MEMORY_BACKEND names durable stores only.
package graphmemorytest

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory"
)

// Fake is an in-process Store. Its zero value is ready to use.
type Fake struct {
	mu    sync.Mutex
	turns map[string]graphmemory.Turn
}

// Upsert stores turns, keeping the first write of each ID.
func (f *Fake) Upsert(_ context.Context, turns []graphmemory.Turn) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.turns == nil {
		f.turns = map[string]graphmemory.Turn{}
	}
	for _, t := range turns {
		if _, ok := f.turns[t.ID]; !ok {
			f.turns[t.ID] = t
		}
	}
	return nil
}

// Search implements the graphmemory.Query semantics.
func (f *Fake) Search(_ context.Context, q graphmemory.Query) ([]graphmemory.Turn, error) {
	q, err := graphmemory.Normalize(q)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	words := Words(q.Text)
	out := []graphmemory.Turn{}
	for _, t := range f.turns {
		if t.AppName != q.AppName || t.UserID != q.UserID {
			continue
		}
		if !q.Since.IsZero() && t.At.Before(q.Since) {
			continue
		}
		if len(q.Entities) > 0 && !slices.ContainsFunc(t.Entities, func(e string) bool { return slices.Contains(q.Entities, e) }) {
			continue
		}
		if len(words) > 0 && !slices.ContainsFunc(Words(t.Text), func(w string) bool { return slices.Contains(words, w) }) {
			continue
		}
		out = append(out, t)
	}
	SortNewestFirst(out)
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// Words splits text into lower-case words, the unit of keyword matching.
func Words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	})
}

// SortNewestFirst orders turns by time descending, then by ID, so results are
// deterministic when timestamps tie.
func SortNewestFirst(turns []graphmemory.Turn) {
	slices.SortFunc(turns, func(a, b graphmemory.Turn) int {
		if c := b.At.Compare(a.At); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
}

// Harness opens stores for the suite.
//
// Open returns a store over a clean, isolated location. Reopen closes s and
// returns a NEW store over the same location: that is the durability check.
type Harness struct {
	Open   func(t *testing.T) graphmemory.Store
	Reopen func(t *testing.T, s graphmemory.Store) graphmemory.Store
}

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func turn(id, user, text string, minutes int) graphmemory.Turn {
	return graphmemory.Turn{
		ID: id, AppName: "ledgerworks", UserID: user, SessionID: "s-" + user,
		Author: "user", Text: text, At: t0.Add(time.Duration(minutes) * time.Minute),
		Entities: graphmemory.ExtractEntities(text),
	}
}

// Fixture is the data every conformance case starts from.
func Fixture() []graphmemory.Turn {
	return []graphmemory.Turn{
		turn("e1", "taras", "Merchant A-114 integration is legacy", 0),
		turn("e2", "taras", "Refund for txn-2026-07-118845 at merchant A-114 is pending", 5),
		turn("e3", "taras", "Ticket LDG-1 is about payout delays", 10),
		turn("e4", "iryna", "Merchant A-114 belongs to my queue too", 15),
	}
}

// Run executes the conformance suite against h.
func Run(t *testing.T, h Harness) {
	t.Helper()
	ctx := context.Background()
	q := func(user string) graphmemory.Query {
		return graphmemory.Query{AppName: "ledgerworks", UserID: user}
	}
	ids := func(turns []graphmemory.Turn) []string {
		out := []string{}
		for _, t := range turns {
			out = append(out, t.ID)
		}
		return out
	}
	seeded := func(t *testing.T) graphmemory.Store {
		t.Helper()
		s := h.Open(t)
		if err := s.Upsert(ctx, Fixture()); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		return s
	}
	search := func(t *testing.T, s graphmemory.Store, qq graphmemory.Query) []string {
		t.Helper()
		nq, err := graphmemory.Normalize(qq)
		if err != nil {
			t.Fatalf("Normalize: %v", err)
		}
		got, err := s.Search(ctx, nq)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if got == nil {
			t.Fatal("Search returned nil, want empty slice")
		}
		return ids(got)
	}
	want := func(t *testing.T, got []string, want ...string) {
		t.Helper()
		if want == nil {
			want = []string{}
		}
		if !slices.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	}

	t.Run("newest first, scoped to user", func(t *testing.T) {
		want(t, search(t, seeded(t), q("taras")), "e3", "e2", "e1")
	})
	t.Run("user isolation", func(t *testing.T) {
		want(t, search(t, seeded(t), q("iryna")), "e4")
	})
	t.Run("app isolation", func(t *testing.T) {
		other := q("taras")
		other.AppName = "other-app"
		want(t, search(t, seeded(t), other))
	})
	t.Run("entity recall", func(t *testing.T) {
		qq := q("taras")
		qq.Entities = []string{"a-114"} // canonicalised by Normalize
		want(t, search(t, seeded(t), qq), "e2", "e1")
	})
	t.Run("keyword recall", func(t *testing.T) {
		qq := q("taras")
		qq.Text = "legacy"
		want(t, search(t, seeded(t), qq), "e1")
	})
	t.Run("keyword is case-insensitive", func(t *testing.T) {
		qq := q("taras")
		qq.Text = "PAYOUT"
		want(t, search(t, seeded(t), qq), "e3")
	})
	t.Run("entity and keyword combine", func(t *testing.T) {
		qq := q("taras")
		qq.Entities = []string{"A-114"}
		qq.Text = "pending"
		want(t, search(t, seeded(t), qq), "e2")
	})
	t.Run("since bound", func(t *testing.T) {
		qq := q("taras")
		qq.Since = t0.Add(5 * time.Minute)
		want(t, search(t, seeded(t), qq), "e3", "e2")
	})
	t.Run("limit", func(t *testing.T) {
		qq := q("taras")
		qq.Limit = 2
		want(t, search(t, seeded(t), qq), "e3", "e2")
	})
	t.Run("no match is empty, not nil", func(t *testing.T) {
		qq := q("taras")
		qq.Text = "nonexistentword"
		want(t, search(t, seeded(t), qq))
	})
	t.Run("upsert is idempotent by ID", func(t *testing.T) {
		s := seeded(t)
		again := Fixture()
		again[0].Text = "rewritten on retry" // a retry must not duplicate or rewrite
		if err := s.Upsert(ctx, again); err != nil {
			t.Fatalf("second Upsert: %v", err)
		}
		got, err := s.Search(ctx, graphmemory.Query{AppName: "ledgerworks", UserID: "taras", Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		want(t, ids(got), "e3", "e2", "e1")
		for _, tr := range got {
			if tr.ID == "e1" && tr.Text != Fixture()[0].Text {
				t.Errorf("e1 text = %q, want first write kept", tr.Text)
			}
		}
	})
	t.Run("turn fields round-trip", func(t *testing.T) {
		got, err := seeded(t).Search(ctx, graphmemory.Query{AppName: "ledgerworks", UserID: "taras", Text: "pending", Limit: 5})
		if err != nil || len(got) != 1 {
			t.Fatalf("Search = %v, %v", got, err)
		}
		w := Fixture()[1]
		g := got[0]
		if g.SessionID != w.SessionID || g.Author != w.Author || g.Text != w.Text || !g.At.Equal(w.At) {
			t.Errorf("got %+v, want %+v", g, w)
		}
		if !slices.Equal(g.Entities, w.Entities) {
			t.Errorf("entities = %v, want %v", g.Entities, w.Entities)
		}
	})
	t.Run("survives reopen", func(t *testing.T) {
		if h.Reopen == nil {
			t.Fatal("Harness.Reopen is required: every MEMORY_BACKEND must be durable")
		}
		s := h.Reopen(t, seeded(t))
		want(t, search(t, s, q("taras")), "e3", "e2", "e1")
	})
}
