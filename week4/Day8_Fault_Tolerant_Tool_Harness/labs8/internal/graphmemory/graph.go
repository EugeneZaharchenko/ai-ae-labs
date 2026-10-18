package graphmemory

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Entity, Relation and KnowledgeGraph are the shapes of the go-sdk reference
// memory MCP server (examples/server/memory), field for field, so any MCP
// client that knows that server reads ours without adaptation.
type Entity struct {
	Name         string   `json:"name"`
	EntityType   string   `json:"entityType"`
	Observations []string `json:"observations"`
}

// Relation is a directed edge between two entities.
type Relation struct {
	From         string `json:"from"`
	To           string `json:"to"`
	RelationType string `json:"relationType"`
}

// KnowledgeGraph is what the recall operations return.
type KnowledgeGraph struct {
	Entities  []Entity   `json:"entities"`
	Relations []Relation `json:"relations"`
	// Truncated reports that the store held more matching turns than Limit.
	Truncated bool `json:"truncated,omitempty"`
}

// The identifiers of the course domain. Regexes, not NLP: extraction must be
// deterministic, because the same session is ingested again on every retry.
//
// Longest first: a refund case id contains a transaction id and a merchant id,
// and whichever pattern matches a span first owns it.
var entityPatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"refund_case", regexp.MustCompile(`(?i)\brc-txn-\d{4}-\d{2}-\d+-[a-z]-\d+\b`)},
	{"transaction", regexp.MustCompile(`(?i)\btxn-\d{4}-\d{2}-\d+\b`)},
	{"ticket", regexp.MustCompile(`(?i)\bLDG-\d+\b`)},
	{"merchant", regexp.MustCompile(`(?i)\b[A-Z]-\d{2,}\b`)},
}

// ExtractEntities returns the canonical identifiers mentioned in text, in
// order of first appearance.
func ExtractEntities(text string) []string {
	type hit struct {
		at   int
		name string
	}
	var hits []hit
	taken := make([]bool, len(text))
	for _, p := range entityPatterns {
		for _, loc := range p.re.FindAllStringIndex(text, -1) {
			if slices.Contains(taken[loc[0]:loc[1]], true) {
				continue // part of a longer identifier matched earlier
			}
			for i := loc[0]; i < loc[1]; i++ {
				taken[i] = true
			}
			hits = append(hits, hit{loc[0], CanonicalEntity(text[loc[0]:loc[1]])})
		}
	}
	slices.SortFunc(hits, func(a, b hit) int { return a.at - b.at })
	var out []string
	for _, h := range hits {
		if !slices.Contains(out, h.name) {
			out = append(out, h.name)
		}
	}
	return out
}

// CanonicalEntity normalises an identifier: merchants and tickets upper case
// ("a-114" → "A-114"), transactions and cases lower case.
func CanonicalEntity(name string) string {
	name = strings.TrimSpace(name)
	switch EntityType(name) {
	case "transaction", "refund_case":
		return strings.ToLower(name)
	case "":
		return name
	default:
		return strings.ToUpper(name)
	}
}

// EntityType classifies a canonical identifier; "" when it is not one.
func EntityType(name string) string {
	for _, p := range entityPatterns {
		if loc := p.re.FindStringIndex(name); loc != nil && loc[0] == 0 && loc[1] == len(name) {
			return p.kind
		}
	}
	return ""
}

// SearchNodes is the reference server's search_nodes over this store: turns
// matching query, folded into the entities they mention.
func (m *Service) SearchNodes(ctx context.Context, app, user, query string, limit int) (KnowledgeGraph, error) {
	q := Query{AppName: app, UserID: user, Text: query, Limit: limit}
	// An identifier is an exact entity lookup, not a keyword.
	if EntityType(CanonicalEntity(query)) != "" {
		q = Query{AppName: app, UserID: user, Entities: []string{query}, Limit: limit}
	}
	return m.graph(ctx, q, nil)
}

// OpenNodes is the reference server's open_nodes: everything remembered about
// the named entities, and the relations among them.
func (m *Service) OpenNodes(ctx context.Context, app, user string, names []string, limit int) (KnowledgeGraph, error) {
	q := Query{AppName: app, UserID: user, Entities: names, Limit: limit}
	keep := make([]string, 0, len(names))
	for _, n := range names {
		keep = append(keep, CanonicalEntity(n))
	}
	return m.graph(ctx, q, keep)
}

// graph runs q and folds the turns into a KnowledgeGraph. When keep is not
// nil, only those entities (and relations between them) are returned.
func (m *Service) graph(ctx context.Context, q Query, keep []string) (KnowledgeGraph, error) {
	nq, err := Normalize(q)
	if err != nil {
		return KnowledgeGraph{}, err
	}
	probe := nq
	probe.Limit = nq.Limit + 1 // one extra row tells us whether we truncated
	turns, err := m.Search(ctx, probe)
	if err != nil {
		return KnowledgeGraph{}, err
	}
	g := KnowledgeGraph{Entities: []Entity{}, Relations: []Relation{}}
	if len(turns) > nq.Limit {
		turns, g.Truncated = turns[:nq.Limit], true
	}
	wanted := func(name string) bool { return keep == nil || slices.Contains(keep, name) }

	index := map[string]int{}
	for _, t := range turns {
		for _, name := range t.Entities {
			if !wanted(name) {
				continue
			}
			i, ok := index[name]
			if !ok {
				i = len(g.Entities)
				index[name] = i
				g.Entities = append(g.Entities, Entity{Name: name, EntityType: EntityType(name), Observations: []string{}})
			}
			g.Entities[i].Observations = append(g.Entities[i].Observations, Observation(t))
		}
		for _, r := range relationsOf(t.Entities) {
			if wanted(r.From) && wanted(r.To) && !slices.Contains(g.Relations, r) {
				g.Relations = append(g.Relations, r)
			}
		}
	}
	return g, nil
}

// Observation renders a turn the way the model should read it: when, who, what.
func Observation(t Turn) string {
	return t.At.UTC().Format(time.RFC3339) + " " + t.Author + ": " + t.Text
}

// relationsOf derives edges from identifiers mentioned together in one turn.
func relationsOf(names []string) []Relation {
	var out []Relation
	for _, a := range names {
		for _, b := range names {
			if a == b {
				continue
			}
			if rt := relationType(EntityType(a), EntityType(b)); rt != "" {
				out = append(out, Relation{From: a, To: b, RelationType: rt})
			}
		}
	}
	return out
}

func relationType(from, to string) string {
	switch {
	case from == "transaction" && to == "merchant":
		return "FOR_MERCHANT"
	case from == "refund_case" && to == "transaction":
		return "FOR_TRANSACTION"
	case from == "ticket" && to != "ticket":
		return "ABOUT"
	}
	return ""
}
