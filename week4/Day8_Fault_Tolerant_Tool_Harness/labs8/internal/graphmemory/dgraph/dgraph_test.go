package dgraph

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dgraph-io/dgo/v250"
	"github.com/dgraph-io/dgo/v250/protos/api"

	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory/graphmemorytest"
)

// fakeClient records requests and answers from a script.
type fakeClient struct {
	alterErr error
	reqs     []*api.Request
	answer   func(n int, req *api.Request) (*api.Response, error)
	closed   bool
	schema   string
}

func (f *fakeClient) Alter(_ context.Context, op *api.Operation) error {
	f.schema = op.Schema
	return f.alterErr
}
func (f *fakeClient) Close() { f.closed = true }
func (f *fakeClient) Do(_ context.Context, req *api.Request) (*api.Response, error) {
	f.reqs = append(f.reqs, req)
	if f.answer == nil {
		return &api.Response{}, nil
	}
	return f.answer(len(f.reqs), req)
}

func newStore(t *testing.T, f *fakeClient) *Store {
	t.Helper()
	s, err := New(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNewAppliesSchema(t *testing.T) {
	t.Parallel()
	f := &fakeClient{}
	s := newStore(t, f)
	if f.schema != Schema {
		t.Error("schema not applied")
	}
	if err := s.Close(); err != nil || !f.closed {
		t.Errorf("Close: %v, closed=%v", err, f.closed)
	}
}

func TestNewClosesOnSchemaError(t *testing.T) {
	t.Parallel()
	f := &fakeClient{alterErr: errors.New("boom")}
	if _, err := New(context.Background(), f); err == nil || !f.closed {
		t.Fatalf("err = %v, closed = %v; want error and closed client", err, f.closed)
	}
}

func TestUpsertBuildsConditionalMutations(t *testing.T) {
	t.Parallel()
	f := &fakeClient{}
	s := newStore(t, f)
	turns := graphmemorytest.Fixture()[:2] // e1: A-114; e2: txn + A-114
	if err := s.Upsert(context.Background(), turns); err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 2 {
		t.Fatalf("requests = %d, want 2 (entities, then turns)", len(f.reqs))
	}

	ents := f.reqs[0]
	if len(ents.Mutations) != 2 || !ents.CommitNow {
		t.Fatalf("entity request = %+v", ents)
	}
	if ents.Vars["$e0"] != "ledgerworks|taras|A-114" || ents.Vars["$e1"] != "ledgerworks|taras|txn-2026-07-118845" {
		t.Errorf("entity vars = %v", ents.Vars)
	}
	if ents.Mutations[1].Cond != "@if(eq(len(e1), 0))" {
		t.Errorf("cond = %q", ents.Mutations[1].Cond)
	}

	tr := f.reqs[1]
	if len(tr.Mutations) != 2 || tr.Vars["$t1"] != "e2" {
		t.Fatalf("turn request vars = %v", tr.Vars)
	}
	for _, m := range tr.Mutations {
		var doc map[string]any
		if err := json.Unmarshal(m.SetJson, &doc); err != nil {
			t.Fatal(err)
		}
		if doc["gm_app"] != "ledgerworks" || doc["dgraph.type"] != "GmTurn" {
			t.Errorf("turn doc = %v", doc)
		}
	}
	var second map[string]any
	_ = json.Unmarshal(tr.Mutations[1].SetJson, &second)
	mentions, _ := second["gm_mentions"].([]any)
	if len(mentions) != 2 || mentions[0].(map[string]any)["uid"] != "uid(e1)" || mentions[1].(map[string]any)["uid"] != "uid(e0)" {
		t.Errorf("mentions = %v (txn is e1, A-114 is e0)", mentions)
	}
	for _, r := range f.reqs {
		if strings.Contains(r.Query, "taras") || strings.Contains(r.Query, "legacy") {
			t.Errorf("user data leaked into DQL text: %s", r.Query)
		}
	}
}

func TestUpsertWithoutEntitiesSkipsEntityRequest(t *testing.T) {
	t.Parallel()
	f := &fakeClient{}
	s := newStore(t, f)
	turn := graphmemory.Turn{ID: "x", AppName: "a", UserID: "u", Text: "no ids here", At: time.Unix(0, 0)}
	if err := s.Upsert(context.Background(), []graphmemory.Turn{turn}); err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 1 || !strings.Contains(f.reqs[0].Query, "t0 as var") {
		t.Fatalf("requests = %+v", f.reqs)
	}
}

func TestUpsertRetriesAbortedTransactions(t *testing.T) {
	t.Parallel()
	f := &fakeClient{answer: func(n int, _ *api.Request) (*api.Response, error) {
		if n == 1 {
			return nil, dgo.ErrAborted
		}
		return &api.Response{}, nil
	}}
	s := newStore(t, f)
	if err := s.Upsert(context.Background(), graphmemorytest.Fixture()[:1]); err != nil {
		t.Fatalf("aborted once then ok: %v", err)
	}
	if len(f.reqs) != 3 {
		t.Errorf("requests = %d, want 3 (entities x2, turns)", len(f.reqs))
	}
}

func TestUpsertGivesUpAfterRetries(t *testing.T) {
	t.Parallel()
	f := &fakeClient{answer: func(int, *api.Request) (*api.Response, error) { return nil, dgo.ErrAborted }}
	s := newStore(t, f)
	err := s.Upsert(context.Background(), graphmemorytest.Fixture()[:1])
	if !errors.Is(err, dgo.ErrAborted) || len(f.reqs) != abortRetries {
		t.Fatalf("err = %v after %d requests", err, len(f.reqs))
	}
}

func TestUpsertStopsOnCancelledContext(t *testing.T) {
	t.Parallel()
	f := &fakeClient{answer: func(int, *api.Request) (*api.Response, error) { return nil, dgo.ErrAborted }}
	s := newStore(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Upsert(ctx, graphmemorytest.Fixture()[:1]); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestUpsertPropagatesTurnError(t *testing.T) {
	t.Parallel()
	boom := errors.New("down")
	f := &fakeClient{answer: func(n int, _ *api.Request) (*api.Response, error) {
		if n == 2 {
			return nil, boom
		}
		return &api.Response{}, nil
	}}
	s := newStore(t, f)
	if err := s.Upsert(context.Background(), graphmemorytest.Fixture()[:1]); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestSearchQueryShape(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 10, 1, 9, 5, 0, 0, time.UTC)
	q, vars := searchQuery(graphmemory.Query{AppName: "ledgerworks", UserID: "taras", Text: "pending", Entities: []string{"A-114"}, Since: since, Limit: 7})
	for _, want := range []string{
		"var(func: eq(gm_entity_key, $k0)) { m0 as ~gm_mentions }",
		"turns(func: uid(m0), orderdesc: gm_at, orderasc: gm_turn_id, first: $first)",
		"anyoftext(gm_text, $text)", "ge(gm_at, $since)", "$first: int",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query lacks %q:\n%s", want, q)
		}
	}
	if vars["$k0"] != "ledgerworks|taras|A-114" || vars["$first"] != "7" || vars["$since"] != "2026-10-01T09:05:00Z" {
		t.Errorf("vars = %v", vars)
	}

	plain, pv := searchQuery(graphmemory.Query{AppName: "a", UserID: "u", Limit: 3})
	if !strings.Contains(plain, "turns(func: eq(gm_app, $app)") || strings.Contains(plain, "$text") || len(pv) != 3 {
		t.Errorf("plain query:\n%s\nvars %v", plain, pv)
	}
}

func TestSearchDecodes(t *testing.T) {
	t.Parallel()
	body := `{"turns":[{"gm_turn_id":"e2","gm_app":"ledgerworks","gm_user":"taras","gm_session_id":"s","gm_author":"user",
		"gm_text":"pending","gm_at":"2026-10-01T09:05:00Z","gm_entities":"[\"txn-2026-07-118845\",\"A-114\"]"},
		{"gm_turn_id":"e9","gm_app":"ledgerworks","gm_user":"taras","gm_text":"no entities","gm_at":"2026-10-01T09:00:00Z"}]}`
	f := &fakeClient{answer: func(int, *api.Request) (*api.Response, error) { return &api.Response{Json: []byte(body)}, nil }}
	s := newStore(t, f)
	got, err := s.Search(context.Background(), graphmemory.Query{AppName: "ledgerworks", UserID: "taras"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "e2" || len(got[0].Entities) != 2 || got[1].Entities != nil {
		t.Fatalf("got %+v", got)
	}
	if !f.reqs[0].ReadOnly {
		t.Error("search must be read-only")
	}
}

func TestSearchErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, err := newStore(t, &fakeClient{}).Search(ctx, graphmemory.Query{}); !errors.Is(err, graphmemory.ErrScope) {
		t.Errorf("unscoped: %v", err)
	}
	down := &fakeClient{answer: func(int, *api.Request) (*api.Response, error) { return nil, errors.New("down") }}
	if _, err := newStore(t, down).Search(ctx, graphmemory.Query{AppName: "a", UserID: "u"}); err == nil {
		t.Error("transport error swallowed")
	}
	for _, body := range []string{`{`, `{"turns":[{"gm_turn_id":"x","gm_entities":"not json"}]}`} {
		bad := &fakeClient{answer: func(int, *api.Request) (*api.Response, error) { return &api.Response{Json: []byte(body)}, nil }}
		if _, err := newStore(t, bad).Search(ctx, graphmemory.Query{AppName: "a", UserID: "u"}); err == nil {
			t.Errorf("body %q: want decode error", body)
		}
	}
	empty, err := newStore(t, &fakeClient{}).Search(ctx, graphmemory.Query{AppName: "a", UserID: "u"})
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("empty response = %v, %v; want [] and nil", empty, err)
	}
}

func TestOpenFailsFastWhenUnreachable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Open(ctx, "127.0.0.1:1"); err == nil {
		t.Fatal("Open must fail when nothing listens")
	}
}
