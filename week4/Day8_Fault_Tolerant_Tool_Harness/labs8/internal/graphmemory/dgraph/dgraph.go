// Package dgraph is the Dgraph backend of graphmemory — the course default.
//
// Graph shape: a GmTurn node per remembered utterance, a GmEntity node per
// identifier per user, and a gm_mentions edge from turn to entity. Entity
// keys are scoped "app|user|name", so two users never share a node.
//
// Every write is a conditional upsert (query + @if mutation), so writing the
// same turn twice is a no-op. Every user value travels as a query variable or
// inside JSON — never spliced into DQL text.
package dgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dgraph-io/dgo/v250"
	"github.com/dgraph-io/dgo/v250/protos/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory"
)

// Schema is applied by Open. Predicates are prefixed gm_ so the memory graph
// can share a Dgraph instance with other data.
const Schema = `
gm_turn_id: string @index(exact) @upsert .
gm_app: string @index(exact) .
gm_user: string @index(exact) .
gm_session_id: string .
gm_author: string .
gm_text: string @index(fulltext) .
gm_at: datetime @index(hour) .
gm_entities: string .
gm_mentions: [uid] @reverse .
gm_entity_key: string @index(exact) @upsert .
gm_entity_name: string @index(exact) .

type GmTurn {
	gm_turn_id
	gm_app
	gm_user
	gm_session_id
	gm_author
	gm_text
	gm_at
	gm_entities
	gm_mentions
}

type GmEntity {
	gm_entity_key
	gm_entity_name
}
`

// batchSize bounds the number of conditional mutations in one request.
const batchSize = 50

// abortRetries is how often an upsert that lost a conflict is retried here,
// before the error is returned (and the surrounding Activity retries it).
const abortRetries = 3

// Client is the slice of dgo the store uses — the seam unit tests fake.
type Client interface {
	Alter(ctx context.Context, op *api.Operation) error
	Do(ctx context.Context, req *api.Request) (*api.Response, error)
	Close()
}

// Store is a graphmemory.Store over Dgraph.
type Store struct {
	c Client
}

var _ graphmemory.Store = (*Store)(nil)

// Open connects to a Dgraph alpha at addr (host:port, gRPC) without TLS,
// fails fast when it is unreachable, and applies Schema.
func Open(ctx context.Context, addr string) (*Store, error) {
	d, err := dgo.NewClient(addr, dgo.WithGrpcOption(grpc.WithTransportCredentials(insecure.NewCredentials())))
	if err != nil {
		return nil, fmt.Errorf("dgraph: connect %s: %w", addr, err)
	}
	return New(ctx, dgoClient{d})
}

// New applies Schema through c and returns the store.
func New(ctx context.Context, c Client) (*Store, error) {
	if err := c.Alter(ctx, &api.Operation{Schema: Schema}); err != nil {
		c.Close()
		return nil, fmt.Errorf("dgraph: apply schema: %w", err)
	}
	return &Store{c: c}, nil
}

// Close releases the connection.
func (s *Store) Close() error {
	s.c.Close()
	return nil
}

// Upsert writes entities first, then turns that mention them.
func (s *Store) Upsert(ctx context.Context, turns []graphmemory.Turn) error {
	for chunk := range slices.Chunk(turns, batchSize) {
		if err := s.withRetry(ctx, func() error { return s.upsertEntities(ctx, chunk) }); err != nil {
			return err
		}
		if err := s.withRetry(ctx, func() error { return s.upsertTurns(ctx, chunk) }); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) withRetry(ctx context.Context, op func() error) error {
	var err error
	for range abortRetries {
		if err = op(); !errors.Is(err, dgo.ErrAborted) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return err
}

func entityKey(t graphmemory.Turn, name string) string {
	return t.AppName + "|" + t.UserID + "|" + name
}

// keys returns the distinct entity keys of turns, in first-seen order.
func keys(turns []graphmemory.Turn) []string {
	var out []string
	for _, t := range turns {
		for _, e := range t.Entities {
			if k := entityKey(t, e); !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
	}
	return out
}

// varBlock declares one query variable and one uid variable per value:
// "query q($k0: string, ...) { e0 as var(func: eq(pred, $k0)) ... }".
func varBlock(pred, prefix string, values []string, vars map[string]string) (decl, body string) {
	var d, b strings.Builder
	for i, v := range values {
		name := fmt.Sprintf("$%s%d", prefix, i)
		vars[name] = v
		fmt.Fprintf(&d, ", %s: string", name)
		fmt.Fprintf(&b, "\t%s%d as var(func: eq(%s, %s))\n", prefix, i, pred, name)
	}
	return d.String(), b.String()
}

func (s *Store) upsertEntities(ctx context.Context, turns []graphmemory.Turn) error {
	ks := keys(turns)
	if len(ks) == 0 {
		return nil
	}
	vars := map[string]string{}
	decl, body := varBlock("gm_entity_key", "e", ks, vars)
	req := &api.Request{
		Query:     "query q(" + strings.TrimPrefix(decl, ", ") + ") {\n" + body + "}",
		Vars:      vars,
		CommitNow: true,
	}
	for i, k := range ks {
		name := k[strings.LastIndex(k, "|")+1:]
		js, err := json.Marshal(map[string]any{
			"uid": fmt.Sprintf("_:e%d", i), "dgraph.type": "GmEntity",
			"gm_entity_key": k, "gm_entity_name": name,
		})
		if err != nil {
			return fmt.Errorf("dgraph: encode entity: %w", err)
		}
		req.Mutations = append(req.Mutations, &api.Mutation{Cond: fmt.Sprintf("@if(eq(len(e%d), 0))", i), SetJson: js})
	}
	if _, err := s.c.Do(ctx, req); err != nil {
		return fmt.Errorf("dgraph: upsert entities: %w", err)
	}
	return nil
}

func (s *Store) upsertTurns(ctx context.Context, turns []graphmemory.Turn) error {
	ks := keys(turns)
	ids := make([]string, len(turns))
	for i, t := range turns {
		ids[i] = t.ID
	}
	vars := map[string]string{}
	tDecl, tBody := varBlock("gm_turn_id", "t", ids, vars)
	eDecl, eBody := varBlock("gm_entity_key", "e", ks, vars)
	req := &api.Request{
		Query:     "query q(" + strings.TrimPrefix(tDecl+eDecl, ", ") + ") {\n" + tBody + eBody + "}",
		Vars:      vars,
		CommitNow: true,
	}
	for i, t := range turns {
		mentions := []map[string]string{}
		for _, e := range t.Entities {
			mentions = append(mentions, map[string]string{"uid": fmt.Sprintf("uid(e%d)", slices.Index(ks, entityKey(t, e)))})
		}
		entities, err := json.Marshal(t.Entities)
		if err != nil {
			return fmt.Errorf("dgraph: encode entities: %w", err)
		}
		js, err := json.Marshal(map[string]any{
			"uid": fmt.Sprintf("_:t%d", i), "dgraph.type": "GmTurn",
			"gm_turn_id": t.ID, "gm_app": t.AppName, "gm_user": t.UserID,
			"gm_session_id": t.SessionID, "gm_author": t.Author, "gm_text": t.Text,
			"gm_at": t.At.UTC().Format(time.RFC3339Nano), "gm_entities": string(entities),
			"gm_mentions": mentions,
		})
		if err != nil {
			return fmt.Errorf("dgraph: encode turn: %w", err)
		}
		req.Mutations = append(req.Mutations, &api.Mutation{Cond: fmt.Sprintf("@if(eq(len(t%d), 0))", i), SetJson: js})
	}
	if _, err := s.c.Do(ctx, req); err != nil {
		return fmt.Errorf("dgraph: upsert turns: %w", err)
	}
	return nil
}

// Search builds one read-only query from q.
func (s *Store) Search(ctx context.Context, q graphmemory.Query) ([]graphmemory.Turn, error) {
	q, err := graphmemory.Normalize(q)
	if err != nil {
		return nil, err
	}
	query, vars := searchQuery(q)
	resp, err := s.c.Do(ctx, &api.Request{Query: query, Vars: vars, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("dgraph: search: %w", err)
	}
	return decodeTurns(resp.GetJson())
}

// searchQuery renders q as DQL. Only structure varies; values are variables.
func searchQuery(q graphmemory.Query) (string, map[string]string) {
	vars := map[string]string{"$app": q.AppName, "$user": q.UserID}
	decl := "$app: string, $user: string"
	var pre strings.Builder
	root := "eq(gm_app, $app)"
	if len(q.Entities) > 0 {
		uids := make([]string, len(q.Entities))
		for i, e := range q.Entities {
			name := fmt.Sprintf("$k%d", i)
			vars[name] = q.AppName + "|" + q.UserID + "|" + e
			decl += ", " + name + ": string"
			fmt.Fprintf(&pre, "\tvar(func: eq(gm_entity_key, %s)) { m%d as ~gm_mentions }\n", name, i)
			uids[i] = fmt.Sprintf("m%d", i)
		}
		root = "uid(" + strings.Join(uids, ", ") + ")"
	}
	filter := "eq(gm_app, $app) AND eq(gm_user, $user)"
	if q.Text != "" {
		vars["$text"] = q.Text
		decl += ", $text: string"
		filter += " AND anyoftext(gm_text, $text)"
	}
	if !q.Since.IsZero() {
		vars["$since"] = q.Since.UTC().Format(time.RFC3339Nano)
		decl += ", $since: string"
		filter += " AND ge(gm_at, $since)"
	}
	vars["$first"] = fmt.Sprint(q.Limit)
	decl += ", $first: int"
	return fmt.Sprintf(`query q(%s) {
%s	turns(func: %s, orderdesc: gm_at, orderasc: gm_turn_id, first: $first) @filter(%s) {
		gm_turn_id
		gm_app
		gm_user
		gm_session_id
		gm_author
		gm_text
		gm_at
		gm_entities
	}
}`, decl, pre.String(), root, filter), vars
}

type turnRow struct {
	ID        string    `json:"gm_turn_id"`
	App       string    `json:"gm_app"`
	User      string    `json:"gm_user"`
	SessionID string    `json:"gm_session_id"`
	Author    string    `json:"gm_author"`
	Text      string    `json:"gm_text"`
	At        time.Time `json:"gm_at"`
	Entities  string    `json:"gm_entities"`
}

func decodeTurns(raw []byte) ([]graphmemory.Turn, error) {
	var res struct {
		Turns []turnRow `json:"turns"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("dgraph: decode search result: %w", err)
		}
	}
	out := make([]graphmemory.Turn, 0, len(res.Turns))
	for _, r := range res.Turns {
		var ents []string
		if r.Entities != "" {
			if err := json.Unmarshal([]byte(r.Entities), &ents); err != nil {
				return nil, fmt.Errorf("dgraph: decode entities of %s: %w", r.ID, err)
			}
		}
		out = append(out, graphmemory.Turn{
			ID: r.ID, AppName: r.App, UserID: r.User, SessionID: r.SessionID,
			Author: r.Author, Text: r.Text, At: r.At.UTC(), Entities: ents,
		})
	}
	return out, nil
}

// dgoClient adapts *dgo.Dgraph to Client: one fresh transaction per request.
type dgoClient struct{ d *dgo.Dgraph }

func (c dgoClient) Alter(ctx context.Context, op *api.Operation) error { return c.d.Alter(ctx, op) }
func (c dgoClient) Close()                                             { c.d.Close() }
func (c dgoClient) Do(ctx context.Context, req *api.Request) (*api.Response, error) {
	if req.ReadOnly {
		return c.d.NewReadOnlyTxn().Do(ctx, req)
	}
	return c.d.NewTxn().Do(ctx, req)
}
