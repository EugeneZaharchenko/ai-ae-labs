package main

import (
	"regexp"
	"slices"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

// Query classes the planner routes on.
const (
	classSingleHop = "single-hop"
	classMultiHop  = "multi-hop"
	classGlobal    = "global"
	classMetadata  = "metadata-filter"
)

// maxSteps is the retrieval budget of one query. One step is one sub-query to
// a retriever (vector + BM25 share a sub-query) or one hop of the traversal.
// Oksana's query needs exactly 5: text search, filter, 3 hops.
const maxSteps = 5

// Filter is the structured part of a query.
type Filter struct {
	Tariffs   []string `json:"tariffs,omitempty"`
	Merchants []string `json:"merchants,omitempty"`
	Contracts []string `json:"contracts,omitempty"`
	NBU       bool     `json:"nbu,omitempty"`
}

// Plan says which retrievers run and with which sub-query.
type Plan struct {
	Query     string `json:"query"`
	Class     string `json:"class"`
	TextQuery string `json:"text_query"`
	Filter    Filter `json:"filter"`
	UseFilter bool   `json:"use_filter"`
	Traverse  bool   `json:"traverse"`
	Budget    int    `json:"budget"`
}

var (
	tariffRE   = regexp.MustCompile(`(?i)\bT-\d+\b`)
	merchRE    = regexp.MustCompile(`(?i)\bA-\d+\b`)
	contractRE = regexp.MustCompile(`(?i)\bCT-\d{4}-\d{3}\b`)
)

func upperAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToUpper(s)
	}
	return slices.Compact(out)
}

func parseFilter(q string) Filter {
	low := strings.ToLower(q)
	return Filter{
		Tariffs:   upperAll(tariffRE.FindAllString(q, -1)),
		Merchants: upperAll(merchRE.FindAllString(q, -1)),
		Contracts: upperAll(contractRE.FindAllString(q, -1)),
		NBU:       strings.Contains(low, "нбу"),
	}
}

// classifyQuery is the rule-based planner. An LLM classifier is the production
// option; rules are enough for this corpus and need no key.
func classifyQuery(q string) string {
	f := parseFilter(q)
	low := strings.ToLower(q)
	signer := strings.Contains(low, "підпис")
	structured := len(f.Tariffs) > 0 || f.NBU
	switch {
	case signer && (structured || len(f.Merchants) > 0):
		return classMultiHop
	case strings.Contains(low, "мерчант") && structured, len(f.Merchants) > 0 && f.NBU:
		return classMetadata
	case len(f.Tariffs)+len(f.Merchants)+len(f.Contracts) == 0 && !f.NBU &&
		containsAny(low, "огляд", "підсум", "загалом", "про що"):
		return classGlobal
	default:
		return classSingleHop
	}
}

// classify emits the plan in an event routed by the query class. It returns
// nil so the node does not send a second, unrouted output.
func classify(ctx agent.Context, query string, emit func(*session.Event) error) (any, error) {
	p := Plan{Query: query, Class: classifyQuery(query), TextQuery: query, Filter: parseFilter(query), Budget: maxSteps}
	p.UseFilter = p.Class == classMetadata
	ev := session.NewEvent(ctx, ctx.InvocationID())
	ev.Routes = []string{p.Class}
	ev.Output = p
	return nil, emit(ev)
}

// decompose splits a multi-hop query: the structured part goes to
// graph_filter, the "who signed" part to graph_traversal, and the text
// search gets the query without the signer clause.
func decompose(_ agent.Context, p Plan) (Plan, error) {
	p.UseFilter = len(p.Filter.Tariffs) > 0 || p.Filter.NBU || len(p.Filter.Merchants) > 0
	p.Traverse = true
	if i := strings.Index(strings.ToLower(p.Query), " і хто"); i > 0 {
		p.TextQuery = p.Query[:i]
	}
	return p, nil
}
