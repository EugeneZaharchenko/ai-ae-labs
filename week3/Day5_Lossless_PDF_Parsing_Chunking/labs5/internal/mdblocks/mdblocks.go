// Package mdblocks splits Markdown into structural blocks: headings,
// paragraphs, lists and tables.
//
// It is the reading layer of the lab, not the lesson. The student's job is
// what happens next — grouping blocks into parent and child chunks — so the
// line-by-line scanning lives here, tested once, and every lab uses the same
// rules.
//
// Two properties matter for lossless chunking:
//
//   - A table is one block. Its Text is the original lines, byte for byte, so a
//     chunk built from it holds the table exactly as the parser produced it.
//     Table also carries the parsed header and rows for entity extraction.
//   - Heading depth comes from section numbering ("2.1 Findings" → 2), not from
//     the count of '#'. docling flattens every heading to "##", so the '#'
//     count of a converted PDF says nothing about the hierarchy.
package mdblocks

import (
	"regexp"
	"strings"
)

// Kind is the type of a block.
type Kind string

// Block kinds.
const (
	KindHeading Kind = "heading"
	KindText    Kind = "text"
	KindList    Kind = "list"
	KindTable   Kind = "table"
)

// Table is a Markdown table with its two-dimensional structure kept.
type Table struct {
	Header []string   `json:"header"`
	Rows   [][]string `json:"rows"`
}

// Cell returns the value at row in the column named column (case-insensitive).
func (t Table) Cell(row int, column string) (string, bool) {
	if row < 0 || row >= len(t.Rows) {
		return "", false
	}
	for i, h := range t.Header {
		if strings.EqualFold(h, column) && i < len(t.Rows[row]) {
			return t.Rows[row][i], true
		}
	}
	return "", false
}

// Column returns every non-empty value of the named column, top to bottom.
func (t Table) Column(column string) []string {
	var out []string
	for i := range t.Rows {
		if v, ok := t.Cell(i, column); ok && v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Block is one structural element of a document.
type Block struct {
	Kind Kind   `json:"kind"`
	Text string `json:"text"` // original Markdown lines of the block
	Line int    `json:"line"` // 1-based line of the first source line

	// Heading fields; zero for other kinds.
	Depth  int    `json:"depth,omitempty"`  // 1 for "2 Findings", 2 for "2.1 …"; 0 if unnumbered
	Number string `json:"number,omitempty"` // "2.1"; empty if unnumbered
	Title  string `json:"title,omitempty"`  // heading text without '#' marks

	// Table is set for KindTable.
	Table *Table `json:"table,omitempty"`
}

var (
	headingRE = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*$`)
	numberRE  = regexp.MustCompile(`^(\d+(?:\.\d+)*)\.?\s+`)
	sepRowRE  = regexp.MustCompile(`^\|[\s:|-]+\|$`)
	listRE    = regexp.MustCompile(`^(?:[-*+]|\d+[.)])\s+`)
)

// Parse splits md into blocks in document order. Blank lines separate
// paragraphs and are not blocks themselves.
func Parse(md string) []Block {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var out []Block
	for i := 0; i < len(lines); {
		line := strings.TrimSpace(lines[i])
		switch {
		case line == "":
			i++
		case headingRE.MatchString(line):
			out = append(out, heading(line, i+1))
			i++
		case isTableLine(line):
			end := scan(lines, i, isTableLine)
			out = append(out, table(lines[i:end], i+1))
			i = end
		case listRE.MatchString(line):
			end := scan(lines, i, func(s string) bool { return listRE.MatchString(s) })
			out = append(out, Block{Kind: KindList, Text: join(lines[i:end]), Line: i + 1})
			i = end
		default:
			end := scan(lines, i, isParagraphLine)
			out = append(out, Block{Kind: KindText, Text: join(lines[i:end]), Line: i + 1})
			i = end
		}
	}
	return out
}

// Headings returns the heading blocks of blocks.
func Headings(blocks []Block) []Block { return filter(blocks, KindHeading) }

// Tables returns the table blocks of blocks.
func Tables(blocks []Block) []Block { return filter(blocks, KindTable) }

func filter(blocks []Block, k Kind) []Block {
	var out []Block
	for _, b := range blocks {
		if b.Kind == k {
			out = append(out, b)
		}
	}
	return out
}

func heading(line string, lineNo int) Block {
	title := headingRE.FindStringSubmatch(line)[1]
	b := Block{Kind: KindHeading, Text: line, Line: lineNo, Title: title}
	if m := numberRE.FindStringSubmatch(title); m != nil {
		b.Number = m[1]
		b.Depth = strings.Count(m[1], ".") + 1
	}
	return b
}

func table(lines []string, lineNo int) Block {
	t := &Table{}
	for n, raw := range lines {
		line := strings.TrimSpace(raw)
		if sepRowRE.MatchString(line) {
			continue
		}
		cells := splitRow(line)
		if n == 0 {
			t.Header = cells
			continue
		}
		t.Rows = append(t.Rows, cells)
	}
	return Block{Kind: KindTable, Text: join(lines), Line: lineNo, Table: t}
}

func splitRow(line string) []string {
	parts := strings.Split(strings.Trim(line, "|"), "|")
	cells := make([]string, len(parts))
	for i, p := range parts {
		cells[i] = strings.TrimSpace(p)
	}
	return cells
}

func isTableLine(s string) bool { return strings.HasPrefix(s, "|") }

func isParagraphLine(s string) bool {
	return s != "" && !headingRE.MatchString(s) && !isTableLine(s) && !listRE.MatchString(s)
}

// scan returns the index of the first line at or after start that keep
// rejects (after trimming spaces).
func scan(lines []string, start int, keep func(string) bool) int {
	i := start
	for i < len(lines) && keep(strings.TrimSpace(lines[i])) {
		i++
	}
	return i
}

func join(lines []string) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(out, "\n")
}
