package main

import (
	"slices"

	"github.com/dimetron/ai-eng-course/labs/week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/kgraph"
	"github.com/dimetron/ai-eng-course/labs/week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/vecindex"
)

// highFindings answers the multi-hop query from the homework:
// Vendor -has_report-> Report, Vendor -has_finding-> Finding -has_severity-> High.
// A vector search cannot do this, it only finds similar text.
func highFindings(g *kgraph.Graph, vendorID, reportID string) []string {
	hasReport := slices.ContainsFunc(g.Neighbors(vendorID, "has_report"), func(n kgraph.Node) bool {
		return n.ID == reportID
	})
	if !hasReport {
		return nil
	}
	var out []string
	for _, f := range g.Neighbors(vendorID, "has_finding") {
		for _, s := range g.Neighbors(f.ID, "has_severity") {
			if s.ID == "severity:high" {
				out = append(out, f.Name)
			}
		}
	}
	slices.Sort(out)
	return out
}

// Retrieved is one search result: the child that matched and its parent
// (the section heading), loaded by ParentID after the search.
type Retrieved struct {
	Child  Chunk
	Parent Chunk
	Score  float64
}

// retrieve searches the children in idx and adds the parent of each hit.
func retrieve(idx *vecindex.Index, chunks []Chunk, query string, k int) []Retrieved {
	byID := make(map[string]Chunk, len(chunks))
	for _, c := range chunks {
		byID[c.ID] = c
	}
	var out []Retrieved
	for _, h := range idx.Search(query, k) {
		child := byID[h.ID]
		out = append(out, Retrieved{Child: child, Parent: byID[child.ParentID], Score: h.Score})
	}
	return out
}
