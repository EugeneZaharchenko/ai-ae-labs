package main

// Retrieval over the LEDGERWORKS knowledge base, carried over from HW6:
// Okapi BM25 over sentence passages + a feature re-ranker (stem overlap,
// figures), and an entity graph (entity → sentences that mention it) for the
// second fan-out branch. No model, no key: the strategy's levers (top_k,
// read_docs, sub-queries) change what the writer sees, deterministically.

import (
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/ledgerkb"
)

// Passage is one piece of evidence with its source document.
type Passage struct {
	DocID  string  `json:"doc_id"`
	Text   string  `json:"text"`
	Source string  `json:"source"` // kb_search, kb_read or graph_lookup
	Score  float64 `json:"score"`
}

// Library holds the indexed corpus.
type Library struct {
	docs     map[string]ledgerkb.Doc
	order    []string
	passages []Passage // sentence passages, Source empty
	bm25     *bm25Index
	entities map[string][]int // lowercase entity → passage indexes
}

// NewLibrary indexes the knowledge base.
func NewLibrary(kb *ledgerkb.KB) *Library {
	lib := &Library{docs: map[string]ledgerkb.Doc{}, entities: map[string][]int{}}
	for _, d := range kb.Docs() {
		lib.docs[d.ID] = d
		lib.order = append(lib.order, d.ID)
		for _, s := range sentences(d.Body) {
			lib.passages = append(lib.passages, Passage{DocID: d.ID, Text: s})
		}
	}
	lib.bm25 = newBM25(lib.passages)
	for i, p := range lib.passages {
		for _, e := range entitiesIn(p.Text) {
			k := strings.ToLower(e)
			if !slices.Contains(lib.entities[k], i) {
				lib.entities[k] = append(lib.entities[k], i)
			}
		}
	}
	return lib
}

// Has reports whether a document id exists.
func (l *Library) Has(id string) bool { _, ok := l.docs[id]; return ok }

// Body returns a document's full text.
func (l *Library) Body(id string) string { return l.docs[id].Body }

// Search returns the top k re-ranked passages for one query.
func (l *Library) Search(query string, k int) []Passage {
	cands := l.bm25.search(query, k*3)
	for i := range cands {
		cands[i].Score = rerankScore(query, cands[i].Text, cands[i].Score)
		cands[i].Source = SourceKB
	}
	sortPassages(cands)
	return cands[:min(k, len(cands))]
}

// Read returns the whole document as one passage (what kb_read gives).
func (l *Library) Read(id string) (Passage, bool) {
	d, ok := l.docs[id]
	if !ok {
		return Passage{}, false
	}
	return Passage{DocID: d.ID, Text: d.Title + ". " + d.Body, Source: "kb_read"}, true
}

// GraphLookup finds entities named in the query and returns the passages that
// mention them, best first, at most k per entity.
func (l *Library) GraphLookup(query string, k int) ([]Passage, []string) {
	q := strings.ToLower(query)
	var found []string
	var out []Passage
	seen := map[int]bool{}
	keys := make([]string, 0, len(l.entities))
	for e := range l.entities {
		keys = append(keys, e)
	}
	sort.Strings(keys)
	for _, e := range keys {
		if !containsWord(q, e) || len(l.entities[e]) > 6 { // too common to be a lead
			continue
		}
		found = append(found, e)
		var idx []int
		for _, i := range l.entities[e] {
			if !seen[i] {
				idx = append(idx, i)
			}
		}
		score := func(i int) float64 { return rerankScore(query, l.passages[i].Text, 1) }
		sort.SliceStable(idx, func(a, b int) bool { return score(idx[a]) > score(idx[b]) })
		for _, i := range idx[:min(k, len(idx))] {
			seen[i] = true
			p := l.passages[i]
			p.Source, p.Score = SourceGraph, score(i)
			out = append(out, p)
		}
	}
	return out, found
}

var (
	acronymRE  = regexp.MustCompile(`\b[A-Z][A-Z0-9+]{1,}\b`)
	codeRE     = regexp.MustCompile(`\b\d{4}\b`)
	properRE   = regexp.MustCompile(`\b[A-Z][a-z]+(?: of| [A-Z][a-z]+)+\b`)
	tariffRE   = regexp.MustCompile(`\b[A-Z][a-z]+ tariff\b`)
	teamRE     = regexp.MustCompile(`\b[A-Z][a-z]+ team\b`)
	sentenceRE = regexp.MustCompile(`\.\s+([A-Z])`)
)

// entitiesIn extracts graph entities: acronyms (SEV1, PCI, UAH), 4-digit
// codes (4837), proper names (Compliance Officer, Head of Payments), tariffs
// and teams.
func entitiesIn(text string) []string {
	var out []string
	for _, re := range []*regexp.Regexp{acronymRE, codeRE, properRE, tariffRE, teamRE} {
		for _, m := range re.FindAllString(text, -1) {
			m = strings.TrimSuffix(strings.TrimSpace(m), " of")
			if m != "" && !slices.Contains(out, m) {
				out = append(out, m)
			}
		}
	}
	return out
}

// sentences splits a policy body into sentences without breaking "1.0%".
func sentences(body string) []string {
	marked := sentenceRE.ReplaceAllString(body, ".\x00$1")
	var out []string
	for s := range strings.SplitSeq(marked, "\x00") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// containsWord reports whether needle occurs in hay at word boundaries.
func containsWord(hay, needle string) bool {
	for i := 0; ; {
		j := strings.Index(hay[i:], needle)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(needle)
		before := start == 0 || !isWordRune(hay[:start])
		after := end == len(hay) || !isWordStart(hay[end:])
		if before && after {
			return true
		}
		i = start + 1
	}
}

func isWordRune(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func isWordStart(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func sortPassages(ps []Passage) {
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Score != ps[j].Score {
			return ps[i].Score > ps[j].Score
		}
		return ps[i].DocID < ps[j].DocID
	})
}

// rerankScore — the HW6 re-ranker features: stem overlap with the query and a
// bonus for passages that carry a figure, on top of the first-stage score.
func rerankScore(query, text string, first float64) float64 {
	s := math.Log1p(first) + 0.6*stemOverlap(query, text)
	if strings.IndexFunc(text, unicode.IsDigit) >= 0 {
		s += 0.2
	}
	return s
}

// tokens — lower case, letters and digits; '-', '+', '%' stay inside a word.
func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '+' && r != '%'
	})
}

var stopwords = map[string]bool{"the": true, "and": true, "what": true, "which": true, "how": true, "does": true,
	"for": true, "this": true, "that": true, "are": true, "with": true, "from": true, "its": true, "who": true,
	"must": true, "after": true, "when": true, "say": true, "our": true, "one": true, "into": true}

// stemOverlap is the share of content words of the query whose first 5
// letters occur in the text: a crude stemmer.
func stemOverlap(query, text string) float64 {
	have := map[string]bool{}
	for _, w := range tokens(text) {
		have[stem(w)] = true
	}
	total, hit := 0, 0
	for _, w := range tokens(query) {
		if utf8.RuneCountInString(w) < 3 || stopwords[w] {
			continue
		}
		total++
		if have[stem(w)] {
			hit++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(hit) / float64(total)
}

func stem(w string) string {
	r := []rune(w)
	return string(r[:min(5, len(r))])
}

// bm25Index is Okapi BM25 over passages (HW6).
type bm25Index struct {
	docs  []Passage
	terms [][]string
	df    map[string]int
	avgdl float64
}

const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

func newBM25(ps []Passage) *bm25Index {
	idx := &bm25Index{docs: ps, df: map[string]int{}}
	total := 0
	for _, p := range ps {
		ts := tokens(p.Text)
		idx.terms = append(idx.terms, ts)
		total += len(ts)
		seen := map[string]bool{}
		for _, t := range ts {
			if !seen[t] {
				idx.df[t]++
				seen[t] = true
			}
		}
	}
	if len(ps) > 0 {
		idx.avgdl = float64(total) / float64(len(ps))
	}
	return idx
}

func (idx *bm25Index) search(query string, k int) []Passage {
	n := float64(len(idx.docs))
	qs := slices.Compact(slices.Sorted(slices.Values(tokens(query))))
	var out []Passage
	for i, ts := range idx.terms {
		tf := map[string]int{}
		for _, t := range ts {
			tf[t]++
		}
		score := 0.0
		for _, q := range qs {
			if stopwords[q] {
				continue
			}
			f := float64(tf[q])
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (n-float64(idx.df[q])+0.5)/(float64(idx.df[q])+0.5))
			score += idf * f * (bm25K1 + 1) / (f + bm25K1*(1-bm25B+bm25B*float64(len(ts))/idx.avgdl))
		}
		if score > 0 {
			p := idx.docs[i]
			p.Score = score
			out = append(out, p)
		}
	}
	sortPassages(out)
	return out[:min(k, len(out))]
}
