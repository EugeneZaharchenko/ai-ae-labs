package graphmemory_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory/graphmemorytest"
)

func TestExtractEntities(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"merchant canonicalised", "merchant a-114 again", []string{"A-114"}},
		{"transaction then merchant", "txn-2026-07-118845 at A-114", []string{"txn-2026-07-118845", "A-114"}},
		{"refund case owns its parts", "case rc-txn-2026-07-118845-A-114 opened", []string{"rc-txn-2026-07-118845-a-114"}},
		{"ticket", "see LDG-1", []string{"LDG-1"}},
		{"dedup keeps first order", "A-114, LDG-7, a-114", []string{"A-114", "LDG-7"}},
		{"nothing", "the merchant is legacy", nil},
		{"one-digit is not a merchant", "plan B-1", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := graphmemory.ExtractEntities(tc.text); !slices.Equal(got, tc.want) {
				t.Errorf("ExtractEntities(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestEntityTypeAndCanonical(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, canon, kind string }{
		{"a-114", "A-114", "merchant"},
		{"TXN-2026-07-1", "txn-2026-07-1", "transaction"},
		{"ldg-3", "LDG-3", "ticket"},
		{"RC-TXN-2026-07-1-A-114", "rc-txn-2026-07-1-a-114", "refund_case"},
		{" legacy ", "legacy", ""},
	}
	for _, tc := range tests {
		if got := graphmemory.CanonicalEntity(tc.in); got != tc.canon {
			t.Errorf("CanonicalEntity(%q) = %q, want %q", tc.in, got, tc.canon)
		}
		if got := graphmemory.EntityType(tc.canon); got != tc.kind {
			t.Errorf("EntityType(%q) = %q, want %q", tc.canon, got, tc.kind)
		}
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	if _, err := graphmemory.Normalize(graphmemory.Query{UserID: "u"}); !errors.Is(err, graphmemory.ErrScope) {
		t.Fatalf("missing app: err = %v, want ErrScope", err)
	}
	if _, err := graphmemory.Normalize(graphmemory.Query{AppName: "a"}); !errors.Is(err, graphmemory.ErrScope) {
		t.Fatalf("missing user: err = %v, want ErrScope", err)
	}
	tests := []struct{ in, want int }{{0, graphmemory.DefaultLimit}, {-3, graphmemory.DefaultLimit}, {7, 7}, {1000, graphmemory.MaxLimit}}
	for _, tc := range tests {
		q, err := graphmemory.Normalize(graphmemory.Query{AppName: "a", UserID: "u", Limit: tc.in})
		if err != nil || q.Limit != tc.want {
			t.Errorf("Limit %d → %d, %v; want %d", tc.in, q.Limit, err, tc.want)
		}
	}
	q, _ := graphmemory.Normalize(graphmemory.Query{AppName: "a", UserID: "u", Text: "  x ", Entities: []string{"a-114", "A-114", " ", "ldg-2"}})
	if q.Text != "x" || !slices.Equal(q.Entities, []string{"A-114", "LDG-2"}) {
		t.Errorf("normalised = %q %v", q.Text, q.Entities)
	}
}

// newSession builds a real ADK session holding the given (author, text)
// events, plus one tool-call event that ingestion must skip.
func newSession(t *testing.T, id string, lines ...[2]string) session.Session {
	t.Helper()
	ctx := context.Background()
	svc := session.InMemoryService()
	created, err := svc.Create(ctx, &session.CreateRequest{AppName: "ledgerworks", UserID: "taras", SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	s := created.Session
	base := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	for i, l := range lines {
		ev := session.NewEvent(ctx, "inv-1")
		ev.ID = id + "-ev" + string(rune('a'+i))
		ev.Author = l[0]
		ev.Timestamp = base.Add(time.Duration(i) * time.Minute)
		ev.LLMResponse = model.LLMResponse{Content: genai.NewContentFromText(l[1], genai.RoleUser)}
		if err := svc.AppendEvent(ctx, s, ev); err != nil {
			t.Fatal(err)
		}
	}
	call := session.NewEvent(ctx, "inv-1")
	call.ID = id + "-call"
	call.Author = "agent"
	call.LLMResponse = model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "open_refund_case"}}}}}
	if err := svc.AppendEvent(ctx, s, call); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, &session.GetRequest{AppName: "ledgerworks", UserID: "taras", SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	return got.Session
}

func TestServiceIngestAndSearch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &graphmemorytest.Fake{}
	m := graphmemory.New(store)
	var _ memory.Service = m

	s := newSession(t, "s1",
		[2]string{"user", "Merchant A-114 integration is legacy"},
		[2]string{"agent", "Opened case rc-txn-2026-07-118845-A-114 for txn-2026-07-118845 at A-114."},
	)
	for range 2 { // second ingestion is what a retried Activity does
		if err := m.AddSessionToMemory(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	all, err := m.Search(ctx, graphmemory.Query{AppName: "ledgerworks", UserID: "taras"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("stored %d turns, want 2 (tool call skipped, retry deduplicated): %+v", len(all), all)
	}

	res, err := m.SearchMemory(ctx, &memory.SearchRequest{AppName: "ledgerworks", UserID: "taras", Query: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Memories) != 1 || res.Memories[0].Author != "user" || res.Memories[0].Content.Parts[0].Text != "Merchant A-114 integration is legacy" {
		t.Fatalf("SearchMemory = %+v", res.Memories)
	}
	if res.Memories[0].Content.Role != genai.RoleUser {
		t.Errorf("role = %q, want user", res.Memories[0].Content.Role)
	}

	if _, err := m.Search(ctx, graphmemory.Query{UserID: "taras"}); !errors.Is(err, graphmemory.ErrScope) {
		t.Errorf("unscoped search err = %v, want ErrScope", err)
	}
}

func TestServiceEmptySessionIsNoop(t *testing.T) {
	t.Parallel()
	m := graphmemory.New(failingStore{})
	if err := m.AddSessionToMemory(context.Background(), newSession(t, "empty")); err != nil {
		t.Fatalf("a session with no text must not touch the store: %v", err)
	}
}

type failingStore struct{}

var errStore = errors.New("store down")

func (failingStore) Upsert(context.Context, []graphmemory.Turn) error { return errStore }
func (failingStore) Search(context.Context, graphmemory.Query) ([]graphmemory.Turn, error) {
	return nil, errStore
}

// nilStore returns nil from Search, which the Service must turn into [].
type nilStore struct{}

func (nilStore) Upsert(context.Context, []graphmemory.Turn) error { return nil }
func (nilStore) Search(context.Context, graphmemory.Query) ([]graphmemory.Turn, error) {
	return nil, nil
}

func TestServiceErrorsAreWrapped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := graphmemory.New(failingStore{})
	if err := m.AddSessionToMemory(ctx, newSession(t, "s", [2]string{"user", "hi"})); !errors.Is(err, errStore) {
		t.Errorf("AddSessionToMemory err = %v, want wrapped store error", err)
	}
	if _, err := m.SearchMemory(ctx, &memory.SearchRequest{AppName: "a", UserID: "u", Query: "x"}); !errors.Is(err, errStore) {
		t.Errorf("SearchMemory err = %v", err)
	}
	if _, err := m.OpenNodes(ctx, "a", "u", []string{"A-114"}, 5); !errors.Is(err, errStore) {
		t.Errorf("OpenNodes err = %v", err)
	}
	if _, err := m.SearchNodes(ctx, "", "u", "x", 5); !errors.Is(err, graphmemory.ErrScope) {
		t.Errorf("SearchNodes unscoped err = %v", err)
	}
	got, err := graphmemory.New(nilStore{}).Search(ctx, graphmemory.Query{AppName: "a", UserID: "u"})
	if err != nil || got == nil {
		t.Errorf("nil from store must become empty slice, got %v, %v", got, err)
	}
}

func TestKnowledgeGraphRecall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &graphmemorytest.Fake{}
	if err := store.Upsert(ctx, graphmemorytest.Fixture()); err != nil {
		t.Fatal(err)
	}
	m := graphmemory.New(store)

	g, err := m.OpenNodes(ctx, "ledgerworks", "taras", []string{"a-114"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Entities) != 1 || g.Entities[0].Name != "A-114" || g.Entities[0].EntityType != "merchant" {
		t.Fatalf("OpenNodes entities = %+v", g.Entities)
	}
	if len(g.Entities[0].Observations) != 2 || len(g.Relations) != 0 {
		t.Errorf("OpenNodes = %+v (relations to unopened nodes must be dropped)", g)
	}
	wantObs := "2026-10-01T09:05:00Z user: Refund for txn-2026-07-118845 at merchant A-114 is pending"
	if g.Entities[0].Observations[0] != wantObs {
		t.Errorf("observation = %q, want %q", g.Entities[0].Observations[0], wantObs)
	}

	both, err := m.OpenNodes(ctx, "ledgerworks", "taras", []string{"A-114", "txn-2026-07-118845"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	wantRel := graphmemory.Relation{From: "txn-2026-07-118845", To: "A-114", RelationType: "FOR_MERCHANT"}
	if !slices.Equal(both.Relations, []graphmemory.Relation{wantRel}) {
		t.Errorf("relations = %+v, want %+v", both.Relations, wantRel)
	}

	byWord, err := m.SearchNodes(ctx, "ledgerworks", "taras", "payout", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(byWord.Entities) != 1 || byWord.Entities[0].Name != "LDG-1" || byWord.Entities[0].EntityType != "ticket" {
		t.Errorf("SearchNodes(payout) = %+v", byWord)
	}

	byID, err := m.SearchNodes(ctx, "ledgerworks", "taras", "a-114", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !byID.Truncated || len(byID.Entities) == 0 {
		t.Errorf("SearchNodes(id, limit 1) = %+v, want truncated", byID)
	}

	none, err := m.SearchNodes(ctx, "ledgerworks", "nobody", "anything", 5)
	if err != nil || none.Entities == nil || none.Relations == nil || len(none.Entities) != 0 {
		t.Errorf("empty graph must have non-nil empty slices: %+v, %v", none, err)
	}
}

func TestTicketRelations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &graphmemorytest.Fake{}
	_ = store.Upsert(ctx, []graphmemory.Turn{{
		ID: "x", AppName: "a", UserID: "u", Author: "agent", At: time.Unix(0, 0),
		Text:     "LDG-9 tracks rc-txn-2026-07-1-A-114 for txn-2026-07-1",
		Entities: graphmemory.ExtractEntities("LDG-9 tracks rc-txn-2026-07-1-A-114 for txn-2026-07-1"),
	}})
	g, err := graphmemory.New(store).SearchNodes(ctx, "a", "u", "tracks", 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []graphmemory.Relation{
		{From: "LDG-9", To: "rc-txn-2026-07-1-a-114", RelationType: "ABOUT"},
		{From: "LDG-9", To: "txn-2026-07-1", RelationType: "ABOUT"},
		{From: "rc-txn-2026-07-1-a-114", To: "txn-2026-07-1", RelationType: "FOR_TRANSACTION"},
	}
	if !slices.Equal(g.Relations, want) {
		t.Errorf("relations = %+v\nwant %+v", g.Relations, want)
	}
}
