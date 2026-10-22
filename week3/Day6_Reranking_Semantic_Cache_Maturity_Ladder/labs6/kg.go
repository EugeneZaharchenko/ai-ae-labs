package main

import (
	"regexp"
	"slices"
	"strings"

	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/corpus"
)

// Span points to the exact piece of a chunk that proves a fact (byte offsets).
type Span struct {
	ChunkID    string `json:"chunk_id"`
	DocumentID string `json:"document_id"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
}

// Edge is one typed relation of the knowledge graph with its provenance.
type Edge struct {
	From   string `json:"from"`
	Rel    string `json:"rel"`
	To     string `json:"to"`
	Source Span   `json:"source"`
}

// kgraph is a small in-memory graph built from the chunks:
// Tariff ←on_tariff─ Merchant ─has_contract→ Contract ─signed_by→ Signer,
// Tariff ─subject_to→ NBU 2026.
type kgraph struct {
	out    map[string][]Edge
	in     map[string][]Edge
	chunks map[string]corpus.Chunk
}

const nbu2026 = "NBU-2026"

var (
	merchantRE = regexp.MustCompile(`Мерчант (A-\d+) обслуговується за тарифом (T-\d+).*?договір (CT-\d{4}-\d{3})`)
	signerRE   = regexp.MustCompile(`Договір (CT-\d{4}-\d{3}) підписа(?:в|ла) ([^,]+), ([^,]+), (\d{2}\.\d{2}\.\d{4})`)
	nbuRE      = regexp.MustCompile(`Вимога НБУ 2026 поширюється на мерчантів із тарифом (T-\d+)`)
)

func buildGraph(chunks []corpus.Chunk) *kgraph {
	g := &kgraph{out: map[string][]Edge{}, in: map[string][]Edge{}, chunks: map[string]corpus.Chunk{}}
	for _, c := range chunks {
		g.chunks[c.ID] = c
		span := func(loc []int) Span {
			return Span{ChunkID: c.ID, DocumentID: c.DocumentID, Start: loc[0], End: loc[1]}
		}
		for _, m := range merchantRE.FindAllStringSubmatchIndex(c.Text, -1) {
			merchant, tariff, contract := c.Text[m[2]:m[3]], c.Text[m[4]:m[5]], c.Text[m[6]:m[7]]
			g.add(Edge{From: merchant, Rel: "on_tariff", To: tariff, Source: span(m)})
			g.add(Edge{From: merchant, Rel: "has_contract", To: contract, Source: span(m)})
		}
		for _, m := range signerRE.FindAllStringSubmatchIndex(c.Text, -1) {
			signer := c.Text[m[4]:m[5]] + ", " + c.Text[m[6]:m[7]] + ", " + c.Text[m[8]:m[9]]
			g.add(Edge{From: c.Text[m[2]:m[3]], Rel: "signed_by", To: signer, Source: span(m)})
		}
		for _, m := range nbuRE.FindAllStringSubmatchIndex(c.Text, -1) {
			g.add(Edge{From: c.Text[m[2]:m[3]], Rel: "subject_to", To: nbu2026, Source: span(m)})
		}
	}
	return g
}

func (g *kgraph) add(e Edge) {
	g.out[e.From] = append(g.out[e.From], e)
	g.in[e.To] = append(g.in[e.To], e)
}

// next returns the edges leaving node with relation rel.
func (g *kgraph) next(node, rel string) []Edge {
	return slices.DeleteFunc(slices.Clone(g.out[node]), func(e Edge) bool { return e.Rel != rel })
}

// prev returns the edges entering node with relation rel.
func (g *kgraph) prev(node, rel string) []Edge {
	return slices.DeleteFunc(slices.Clone(g.in[node]), func(e Edge) bool { return e.Rel != rel })
}

// candidate turns the chunk behind a span into a retrieval candidate.
func (g *kgraph) candidate(s Span, score float64) Candidate {
	c := g.chunks[s.ChunkID]
	return Candidate{ChunkID: c.ID, DocumentID: c.DocumentID, SectionPath: c.SectionPath, Text: c.Text, Score: score}
}

// quote returns the text a span points to.
func (g *kgraph) quote(s Span) string {
	return strings.TrimSpace(g.chunks[s.ChunkID].Text[s.Start:s.End])
}
