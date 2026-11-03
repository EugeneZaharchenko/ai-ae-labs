package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// The LEDGERWORKS policy knowledge base the worker agent researches.
//
// It is built so that a strategy can be right or wrong about something
// measurable. kb_search returns only each document's SCOPE sentence — which
// never contains a figure — and kb_read returns the full text. A worker that
// answers from search results alone cannot be correct; one that opens the
// document can. That gap is the lever the loop is allowed to find, and the
// reason a one-line change to strategy.md moves the metric.
//
// Same shape as the labs' internal/ledgerkb (kb_search, kb_read), compacted to
// three documents so the whole thing is readable in one screen.

// Doc is one policy document.
type Doc struct {
	ID    string
	Title string
	Scope string // one sentence, no figures: what search returns
	Body  string // the figures: what kb_read returns
}

var corpus = []Doc{
	{
		ID: "refunds", Title: "Refund policy",
		Scope: "Rules for refunding a cardholder's transaction at a merchant's request.",
		Body: "A refund above 5,000 EUR needs a second approval from the compliance officer. " +
			"Every approved refund is processed within 5 business days. " +
			"A refund can be requested only within 120 days of the transaction.",
	},
	{
		ID: "chargebacks", Title: "Chargeback handling",
		Scope: "How merchant evidence is collected and disputes are routed after a cardholder chargeback.",
		Body: "Merchant evidence must be submitted within 7 calendar days of the notice. " +
			"A chargeback fee of 15 EUR is charged to the merchant. " +
			"Disputes with reason code 4837 are handled by the fraud team.",
	},
	{
		ID: "payouts", Title: "Payout schedule",
		Scope: "When settled funds reach a merchant's account, by tariff, and the instant payout option.",
		Body: "A standard payout for the Growth tariff arrives T+2. " +
			"The daily cut-off for payout instructions is 14:00. " +
			"An instant payout costs 1.0% of the amount.",
	},
}

// Hit is one search result. Snippet is the scope sentence, never the body.
type Hit struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}

// KB is a searchable view of the corpus. It counts reads so the report can say
// whether a strategy actually opened documents, rather than infer it from prose.
type KB struct {
	mu    sync.Mutex
	reads int
}

func NewKB() *KB { return &KB{} }

// Reads is how many kb_read calls were served.
func (k *KB) Reads() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.reads
}

// Search returns the best two documents by distinct-word overlap with the
// query, ties broken by corpus order. Deterministic on purpose: the metric must
// not move because the retriever did.
func (k *KB) Search(query string) []Hit {
	q := words(query)
	type scored struct {
		d Doc
		n int
	}
	var all []scored
	for _, d := range corpus {
		have := words(d.Title + " " + d.Scope + " " + d.Body)
		n := 0
		for w := range q {
			if have[w] {
				n++
			}
		}
		all = append(all, scored{d, n})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].n > all[j].n })
	var hits []Hit
	for _, s := range all {
		if s.n == 0 || len(hits) == 2 {
			break
		}
		hits = append(hits, Hit{ID: s.d.ID, Title: s.d.Title, Snippet: s.d.Scope})
	}
	return hits
}

// Read returns a document's full text.
func (k *KB) Read(id string) (string, bool) {
	for _, d := range corpus {
		if d.ID == id {
			k.mu.Lock()
			k.reads++
			k.mu.Unlock()
			return d.Title + ". " + d.Scope + " " + d.Body, true
		}
	}
	return "", false
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) >= 4 {
			out[w] = true
		}
	}
	return out
}

// Tool names, shared with the strategy allowlist.
const (
	ToolSearch = "kb_search"
	ToolRead   = "kb_read"
)

type SearchArgs struct {
	Query string `json:"query" jsonschema:"what to look for in the policy knowledge base"`
}

type SearchResult struct {
	Hits []Hit `json:"hits"`
}

type ReadArgs struct {
	ID string `json:"id" jsonschema:"document id from a kb_search hit, e.g. refunds"`
}

type ReadResult struct {
	Text string `json:"text"`
}

// Tools builds exactly the named tools. A strategy that lists a tool the KB
// does not have is a configuration error, not something to ignore: silently
// dropping it would make the strategy mean something other than what it says.
func (k *KB) Tools(names []string) ([]tool.Tool, error) {
	var out []tool.Tool
	for _, name := range names {
		switch name {
		case ToolSearch:
			t, err := functiontool.New(functiontool.Config{
				Name:        ToolSearch,
				Description: "Searches the LEDGERWORKS policy documents. Returns the best matches with a one-sentence scope each — NOT the policy text itself.",
			}, func(_ agent.Context, in SearchArgs) (SearchResult, error) {
				return SearchResult{Hits: k.Search(in.Query)}, nil
			})
			if err != nil {
				return nil, fmt.Errorf("build %s: %w", name, err)
			}
			out = append(out, t)
		case ToolRead:
			t, err := functiontool.New(functiontool.Config{
				Name:        ToolRead,
				Description: "Returns the full text of one policy document by id (from a kb_search hit).",
			}, func(_ agent.Context, in ReadArgs) (ReadResult, error) {
				text, ok := k.Read(in.ID)
				if !ok {
					return ReadResult{}, fmt.Errorf("no document %q", in.ID)
				}
				return ReadResult{Text: text}, nil
			})
			if err != nil {
				return nil, fmt.Errorf("build %s: %w", name, err)
			}
			out = append(out, t)
		default:
			return nil, fmt.Errorf("unknown tool %q", name)
		}
	}
	return out, nil
}
