package mdblocks

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestParse_Kinds(t *testing.T) {
	t.Parallel()
	md := "# Title\n\nfirst line\nsecond line\n\n- a\n- b\n1. c\n\n| H1 | H2 |\n|---|:-:|\n| x | y |\n\ntail"
	got := Parse(md)
	want := []struct {
		kind Kind
		line int
		text string
	}{
		{KindHeading, 1, "# Title"},
		{KindText, 3, "first line\nsecond line"},
		{KindList, 6, "- a\n- b\n1. c"},
		{KindTable, 10, "| H1 | H2 |\n|---|:-:|\n| x | y |"},
		{KindText, 14, "tail"},
	}
	if len(got) != len(want) {
		t.Fatalf("Parse() = %d blocks, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Kind != w.kind || got[i].Line != w.line || got[i].Text != w.text {
			t.Errorf("block %d = {%s L%d %q}, want {%s L%d %q}", i, got[i].Kind, got[i].Line, got[i].Text, w.kind, w.line, w.text)
		}
	}
}

func TestParse_Empty(t *testing.T) {
	t.Parallel()
	if got := Parse(" \n\r\n\t\n"); len(got) != 0 {
		t.Errorf("Parse(blank) = %+v, want none", got)
	}
}

func TestHeadingDepth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		line   string
		title  string
		number string
		depth  int
	}{
		{"# LEDGERWORKS — Review", "LEDGERWORKS — Review", "", 0},
		{"## 1 Scope", "1 Scope", "1", 1},
		{"## 1.1 Vendors in scope", "1.1 Vendors in scope", "1.1", 2},
		{"### 2.1. Security ###", "2.1. Security", "2.1", 2},
		{"#### 3.2.1 Deep", "3.2.1 Deep", "3.2.1", 3},
		{"## 2025 in review", "2025 in review", "2025", 1},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			t.Parallel()
			b := Parse(tt.line)[0]
			if b.Title != tt.title || b.Number != tt.number || b.Depth != tt.depth {
				t.Errorf("heading = {%q %q %d}, want {%q %q %d}", b.Title, b.Number, b.Depth, tt.title, tt.number, tt.depth)
			}
		})
	}
}

func TestTable(t *testing.T) {
	t.Parallel()
	b := Parse("| Vendor | Findings |\n|---|---|\n| Acme | 3 |\n| Globex | |\n| Short |")[0]
	if b.Table == nil {
		t.Fatal("Table = nil")
	}
	tb := *b.Table
	if !reflect.DeepEqual(tb.Header, []string{"Vendor", "Findings"}) {
		t.Errorf("Header = %q", tb.Header)
	}
	cells := []struct {
		row    int
		col    string
		want   string
		wantOK bool
	}{
		{0, "vendor", "Acme", true},
		{0, "Findings", "3", true},
		{2, "Findings", "", false}, // short row
		{0, "Missing", "", false},
		{-1, "Vendor", "", false},
		{9, "Vendor", "", false},
	}
	for _, c := range cells {
		if got, ok := tb.Cell(c.row, c.col); got != c.want || ok != c.wantOK {
			t.Errorf("Cell(%d, %q) = %q, %v; want %q, %v", c.row, c.col, got, ok, c.want, c.wantOK)
		}
	}
	if got := tb.Column("Findings"); !reflect.DeepEqual(got, []string{"3"}) {
		t.Errorf("Column(Findings) = %q, want [3] (empty cells skipped)", got)
	}
}

// The real docling output and the hand-written Markdown must give the same
// hierarchy: that is the reason Depth reads numbering, not '#'.
func TestDoclingAndMarkdownAgree(t *testing.T) {
	t.Parallel()
	depths := func(path string) []int {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var out []int
		for _, h := range Headings(Parse(string(data))) {
			out = append(out, h.Depth)
		}
		return out
	}
	md := depths("../../testdata/ledgerworks_soc2.md")
	doc := depths("../../testdata/ledgerworks_soc2.docling.md")
	want := []int{0, 1, 2, 2, 1, 2, 2, 1, 2, 2, 1}
	if !reflect.DeepEqual(md, want) || !reflect.DeepEqual(doc, want) {
		t.Errorf("depths: md=%v docling=%v, want %v", md, doc, want)
	}

	data, err := os.ReadFile("../../testdata/ledgerworks_soc2.docling.md")
	if err != nil {
		t.Fatal(err)
	}
	tables := Tables(Parse(string(data)))
	if len(tables) != 3 {
		t.Fatalf("tables = %d, want 3", len(tables))
	}
	if got := tables[1].Table.Column("Severity"); strings.Join(got, ",") != "High,Medium,High,High,Low" {
		t.Errorf("Severity column = %q", got)
	}
}
