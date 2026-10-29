// Package ledgerkb is the knowledge base and the eval set of the Day 12 lab.
//
// The lab improves a research agent's strategy against ONE metric, so the
// metric has to be earned honestly: a labelled eval set, an answer, and a pure
// scoring function with no model in it. This package holds all three, plus the
// two tools (kb_search, kb_read) the agent researches with.
//
// The corpus is synthetic LEDGERWORKS policy text (the course's fictional
// payments company). That is deliberate: a public corpus is in the training
// data of every model, so a high score would measure memory, not research.
// The shape is real, though — short scope boilerplate up front, the figures
// that matter deeper in the body — because that is exactly how internal
// runbooks look, and it is what makes "search once, answer in one sentence"
// a weak strategy worth improving.
//
// Search is keyword overlap with no stemmer, so the corpus is English; the
// bonus track swaps in embeddings.
package ledgerkb

import (
	"sort"
	"strings"
	"unicode"
)

// Doc is one policy document.
type Doc struct {
	ID    string
	Title string
	Body  string
}

// Hit is one kb_search result. Snippet is only the head of the body: the
// scope paragraph, not the figures.
type Hit struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}

// SnippetRunes is how much of a body kb_search shows.
const SnippetRunes = 140

// KB is a read-only document set.
type KB struct {
	docs []Doc
}

// New builds a KB over the embedded corpus.
func New() *KB { return &KB{docs: corpus()} }

// NewFrom builds a KB over docs, for tests.
func NewFrom(docs []Doc) *KB { return &KB{docs: docs} }

// Docs returns a copy of the corpus.
func (k *KB) Docs() []Doc { return append([]Doc(nil), k.docs...) }

// Get returns the document with the given ID.
func (k *KB) Get(id string) (Doc, bool) {
	id = strings.ToUpper(strings.TrimSpace(id))
	for _, d := range k.docs {
		if d.ID == id {
			return d, true
		}
	}
	return Doc{}, false
}

var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "of": true, "to": true,
	"in": true, "on": true, "for": true, "is": true, "are": true, "do": true, "does": true,
	"what": true, "which": true, "who": true, "how": true, "when": true, "can": true,
	"we": true, "our": true, "it": true, "its": true, "be": true, "by": true, "with": true,
	"that": true, "this": true, "at": true, "as": true, "if": true, "from": true, "than": true,
	"apply": true, "applies": true, "long": true, "much": true, "many": true,
}

func terms(s string) []string {
	f := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := map[string]bool{}
	var out []string
	for _, w := range f {
		if len(w) < 2 || stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// Search ranks documents by how many distinct query terms occur in them (a
// title match counts double) and returns at most k hits. Ties keep corpus
// order, so the same query always returns the same list.
func (kb *KB) Search(query string, k int) []Hit {
	qs := terms(query)
	type scored struct {
		d     Doc
		score int
		pos   int
	}
	var all []scored
	for i, d := range kb.docs {
		title := strings.ToLower(d.Title)
		body := strings.ToLower(d.Body)
		s := 0
		for _, q := range qs {
			if strings.Contains(title, q) {
				s += 2
			}
			if strings.Contains(body, q) {
				s++
			}
		}
		if s > 0 {
			all = append(all, scored{d, s, i})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })
	if k > len(all) {
		k = len(all)
	}
	hits := make([]Hit, 0, k)
	for _, s := range all[:k] {
		hits = append(hits, Hit{ID: s.d.ID, Title: s.d.Title, Snippet: head(s.d.Body, SnippetRunes)})
	}
	return hits
}

func head(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
