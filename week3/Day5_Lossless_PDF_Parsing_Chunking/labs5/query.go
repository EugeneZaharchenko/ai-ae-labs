package main

import (
	"slices"

	"github.com/dimetron/ai-eng-course/labs/week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/kgraph"
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
