package main

import (
	"regexp"
	"strings"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/mdblocks"
)

// entityColumns says which table column holds which entity type.
// The first part is for the LEDGERWORKS report, the second for NIST AI RMF
// (my own PDF), where tables have "Categories" and "Subcategories".
var entityColumns = map[string]string{
	"vendor":        "Vendor",
	"report":        "Report",
	"period":        "Period",
	"finding id":    "Finding",
	"control id":    "Control",
	"severity":      "Severity",
	"merchant":      "Merchant",
	"tariff":        "Tariff",
	"contract":      "Contract",
	"categories":    "Category",
	"subcategories": "Subcategory",
}

// relations between entities found in the same table row: {from, to} → rel.
var relations = map[[2]string]string{
	{"Vendor", "Report"}:        "has_report",
	{"Vendor", "Period"}:        "audited_for",
	{"Vendor", "Finding"}:       "has_finding",
	{"Finding", "Severity"}:     "has_severity",
	{"Finding", "Control"}:      "violates",
	{"Merchant", "Tariff"}:      "on_tariff",
	{"Merchant", "Contract"}:    "has_contract",
	{"Category", "Subcategory"}: "has_subcategory",
}

// NIST cells look like "GOVERN 1.1: Legal and regulatory ...", the id is
// the part before ':'.
var nistIDRE = regexp.MustCompile(`^(GOVERN|MAP|MEASURE|MANAGE)\s+\d+(\.\d+)?`)

// extractEntities reads entities from table chunks only: there the structure
// is already known, so no LLM or guessing is needed. Every entity keeps the
// chunk id it came from (provenance).
func extractEntities(_ agent.Context, b Batch) (Batch, error) {
	var out []Entity
	seen := map[string]bool{} // entity id + chunk id
	for _, c := range b.Chunks {
		for _, row := range rowEntities(c) {
			for _, e := range row {
				key := e.ID + "|" + e.SourceChunkID
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, e)
			}
		}
	}
	b.Entities = out
	return b, nil
}

// rowEntities returns the entities of every row of a table chunk.
// Non-table chunks give nothing.
func rowEntities(c Chunk) [][]Entity {
	if c.Kind != string(mdblocks.KindTable) {
		return nil
	}
	blocks := mdblocks.Parse(c.Text)
	if len(blocks) == 0 || blocks[0].Table == nil {
		return nil
	}
	t := blocks[0].Table
	var rows [][]Entity
	for i := range t.Rows {
		var row []Entity
		for _, h := range t.Header {
			typ, ok := entityColumns[strings.ToLower(strings.TrimSpace(h))]
			if !ok {
				continue
			}
			cell, _ := t.Cell(i, h)
			name := entityName(typ, cell)
			if name == "" {
				continue
			}
			row = append(row, Entity{
				ID:            entityID(typ, name),
				Type:          typ,
				Name:          name,
				SourceChunkID: c.ID,
			})
		}
		rows = append(rows, row)
	}
	return rows
}

func entityName(typ, cell string) string {
	cell = strings.TrimSpace(cell)
	if typ == "Category" || typ == "Subcategory" {
		return nistIDRE.FindString(cell) // only the id, the text is too long
	}
	return cell
}

// entityID is stable: "vendor:acme-bank-jsc".
func entityID(typ, name string) string {
	slug := strings.Join(strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r > 127)
	}), "-")
	return strings.ToLower(typ) + ":" + slug
}
