// HW5 tests: table tests for every node on agent.StrictContextMock,
// no Python, no docling, no network. The PDF path uses fakeConverter;
// the real docling output for the course PDF is in
// testdata/ledgerworks_soc2.docling.md.
//
// The main contract is TestChunk_TableStaysWhole: every table is exactly one
// chunk, with the header and all rows.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/chunkeval"
)

// nodeCtx — суворий фейк agent.Context. Вбудований StrictContextMock означає:
// будь-який метод, який ваш вузол викличе, а ми його тут не передбачили,
// впаде з panic, а не поверне тихо нульове значення.
type nodeCtx struct {
	agent.StrictContextMock
}

func newNodeCtx() *nodeCtx {
	return &nodeCtx{StrictContextMock: agent.NewStrictContextMock(context.Background())}
}

// fakeConverter підміняє docling-mcp: повертає заданий Markdown або помилку.
type fakeConverter struct {
	markdown string
	err      error
}

func (f fakeConverter) ToMarkdown(context.Context, string) (string, error) {
	return f.markdown, f.err
}

const (
	fixtureMD     = "testdata/ledgerworks_soc2.md"
	fixturePDF    = "testdata/ledgerworks_soc2.pdf"
	fixtureDocMD  = "testdata/ledgerworks_soc2.docling.md"
	fixtureTables = 3
)

// --- Вузол load --------------------------------------------------------------

func TestLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	docx := filepath.Join(dir, "doc.docx")
	if err := os.WriteFile(docx, []byte("PK\x03\x04"), 0o600); err != nil {
		t.Fatalf("підготовка фікстури: %v", err)
	}

	tests := []struct {
		name     string
		path     string
		wantKind string
		wantErr  bool
	}{
		{name: "markdown", path: fixtureMD, wantKind: "markdown"},
		{name: "pdf за magic bytes", path: fixturePDF, wantKind: "pdf"},
		{name: "пробіли навколо шляху", path: "  " + fixtureMD + "\n", wantKind: "markdown"},
		{name: "файла немає", path: filepath.Join(dir, "ghost.md"), wantErr: true},
		{name: "порожній шлях", path: "", wantErr: true},
		{name: "непідтримуваний формат", path: docx, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := load(newNodeCtx(), tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("load(%q) = %+v, err = nil; очікували помилку", tt.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("load(%q) error = %v", tt.path, err)
			}
			if got.Kind != tt.wantKind {
				t.Errorf("load(%q).Kind = %q, очікували %q", tt.path, got.Kind, tt.wantKind)
			}
		})
	}
}

// --- Вузол parse -------------------------------------------------------------

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		conv     fakeConverter
		src      Source
		contains string
		wantErr  string
	}{
		{name: "markdown читається як є", src: Source{Path: fixtureMD, Kind: "markdown"}, contains: "| F-101"},
		{name: "pdf іде в конвертер", conv: fakeConverter{markdown: "| a |\n|---|\n| 1 |"}, src: Source{Path: fixturePDF, Kind: "pdf"}, contains: "| 1 |"},
		{name: "помилка конвертера", conv: fakeConverter{err: errors.New("docling down")}, src: Source{Path: fixturePDF, Kind: "pdf"}, wantErr: "docling down"},
		{name: "порожній результат", conv: fakeConverter{markdown: "  \n"}, src: Source{Path: fixturePDF, Kind: "pdf"}, wantErr: "порожній"},
		{name: "markdown-файла немає", src: Source{Path: "testdata/ghost.md", Kind: "markdown"}, wantErr: "ghost.md"},
		{name: "невідомий тип", src: Source{Path: fixtureMD, Kind: "docx"}, wantErr: "невідомий тип"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := newParse(tt.conv)(newNodeCtx(), tt.src)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parse() err = %v, очікували %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse() error = %v", err)
			}
			if got.ID != "ledgerworks_soc2" {
				t.Errorf("Document.ID = %q, очікували ім'я файла без розширення", got.ID)
			}
			if !strings.Contains(got.Markdown, tt.contains) {
				t.Errorf("Markdown не містить %q", tt.contains)
			}
		})
	}
}

// TestDoclingFixtureKeepsTables фіксує, що дає docling на тестовому PDF: усі
// три таблиці — цілими, із заголовками. Зверніть увагу на пастку: docling
// сплющує рівні заголовків — і "1 Scope", і "1.1 Vendors in scope" стають
// "##". Ієрархію для Parent-Child доведеться відновлювати з нумерації розділів.
func TestDoclingFixtureKeepsTables(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(fixtureDocMD)
	if err != nil {
		t.Fatal(err)
	}
	md := string(data)
	if got := strings.Count(md, "\n|--"); got != fixtureTables {
		t.Errorf("таблиць у виході docling = %d, очікували %d", got, fixtureTables)
	}
	if strings.Contains(md, "\n### ") {
		t.Log("docling повернув ### — рівні заголовків збережено; перевірте свою логіку ієрархії")
	}
}

// --- Вузол chunk -------------------------------------------------------------

// TestChunk_Hierarchy checks parents, children, parent_id and section_path.
// The headings are all "##" like in docling output, the depth comes from the numbers.
func TestChunk_Hierarchy(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("Речення з п'яти слів. ", 60) // 300 tokens
	tests := []struct {
		name         string
		md           string
		wantParents  int
		wantChildren int
		wantPath     string // section path of the last child
	}{
		{"one section", "## 1 Scope\n\nабзац\n", 1, 1, "1 Scope"},
		{"subsection is not a parent", "## 1 Scope\n\nабзац\n\n## 1.1 Vendors\n\n- a\n- b\n", 1, 2, "1 Scope / 1.1 Vendors"},
		{"third level", "## 1 A\n\n## 1.2 B\n\n## 1.2.1 C\n\nтекст\n", 1, 1, "1 A / 1.2 B / 1.2.1 C"},
		{"back to second level", "## 1 A\n\n## 1.1 B\n\n## 1.1.1 C\n\n## 1.2 D\n\nтекст\n", 1, 1, "1 A / 1.2 D"},
		{"two sections", "## 1 A\n\nx\n\n## 2 B\n\ny\n", 2, 2, "2 B"},
		{"text before heading gets a parent", "вступ\n\n## 1 A\n\nx\n", 2, 2, "1 A"},
		{"long text is split", "## 1 A\n\n" + long + "\n", 1, 2, "1 A"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := chunk(newNodeCtx(), newDocument("d1", "", tt.md))
			if err != nil {
				t.Fatalf("chunk() error = %v", err)
			}
			parents := map[string]bool{}
			var children []Chunk
			for _, c := range got.Chunks {
				if c.DocumentID != "d1" {
					t.Errorf("chunk %s: DocumentID = %q — provenance втрачено", c.ID, c.DocumentID)
				}
				if c.ParentID == "" {
					parents[c.ID] = true
					continue
				}
				children = append(children, c)
			}
			if len(parents) != tt.wantParents || len(children) != tt.wantChildren {
				t.Fatalf("parents/children = %d/%d, want %d/%d: %+v", len(parents), len(children), tt.wantParents, tt.wantChildren, got.Chunks)
			}
			for _, c := range children {
				if !parents[c.ParentID] {
					t.Errorf("child %s has unknown parent %q", c.ID, c.ParentID)
				}
				if c.Level != 2 || c.SectionPath == "" {
					t.Errorf("child %s: level=%d path=%q", c.ID, c.Level, c.SectionPath)
				}
				if n := chunkeval.Tokens(c.Text); c.Kind != "table" && n > maxChildTokens {
					t.Errorf("child %s has %d tokens > %d", c.ID, n, maxChildTokens)
				}
			}
			if last := children[len(children)-1]; last.SectionPath != tt.wantPath {
				t.Errorf("section_path = %q, want %q", last.SectionPath, tt.wantPath)
			}
		})
	}
}

// A big table stays one child even if it is longer than maxChildTokens.
func TestChunk_BigTableNotSplit(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	sb.WriteString("## 1 A\n\n| ID | Text |\n|----|------|\n")
	for i := range 80 {
		fmt.Fprintf(&sb, "| R-%d | some long cell text here |\n", i)
	}
	got, err := chunk(newNodeCtx(), newDocument("d1", "", sb.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Chunks) != 2 || got.Chunks[1].Kind != "table" {
		t.Fatalf("want 1 parent + 1 table child, got %+v", got.Chunks)
	}
	if !strings.Contains(got.Chunks[1].Text, "| ID | Text |") || !strings.Contains(got.Chunks[1].Text, "R-79") {
		t.Error("table lost its header or last row")
	}
}

func TestChunk_EmptyDocumentIsError(t *testing.T) {
	t.Parallel()
	if _, err := chunk(newNodeCtx(), Document{}); err == nil {
		t.Fatal("chunk(порожній) = nil error; порожній документ має бути помилкою")
	}
}

// TestChunk_TableStaysWhole — КОНТРАКТ ДЗ, а не заготовка.
//
// Головна вимога тижня: жодна таблиця не може бути розрізана між двома
// чанками. Тест перевіряє це напряму на реальному виході docling: кожна
// таблиця — рівно один чанк Kind == "table", з усіма рядками й заголовком.
func TestChunk_TableStaysWhole(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(fixtureDocMD)
	if err != nil {
		t.Fatal(err)
	}
	got, err := chunk(newNodeCtx(), newDocument("ledgerworks_soc2", fixtureDocMD, string(data)))
	if err != nil {
		t.Fatalf("chunk() error = %v", err)
	}

	var tables []Chunk
	for _, c := range got.Chunks {
		if c.Kind == "table" {
			tables = append(tables, c)
		}
	}
	if len(tables) != fixtureTables {
		t.Fatalf("чанків типу table = %d, очікували %d — таблицю розрізано або втрачено", len(tables), fixtureTables)
	}
	wantRows := map[string][]string{
		"Vendor":     {"Acme Bank JSC", "Northwind Cloud", "Globex Payments"},
		"Finding ID": {"F-101", "F-102", "F-103", "F-104", "F-105", "Severity"},
		"Merchant":   {"A-114", "A-207", "A-331", "NBU 2026 requirement"},
	}
	for header, rows := range wantRows {
		var whole bool
		for _, c := range tables {
			if !strings.HasPrefix(firstTableRow(c.Text), "| "+header) {
				continue
			}
			whole = true
			for _, row := range rows {
				if !strings.Contains(c.Text, row) {
					whole = false
					t.Errorf("у таблиці %q немає %q — рядок або заголовок втрачено:\n%s", header, row, c.Text)
				}
			}
			if c.ParentID == "" {
				t.Errorf("таблиця %q без ParentID — зв'язок із розділом втрачено", header)
			}
		}
		if !whole {
			t.Errorf("таблицю з заголовком %q не знайдено цілою", header)
		}
	}
}

// firstTableRow — перший рядок таблиці (рядок заголовків), щоб "Vendor" у
// таблиці знахідок не сплутати з таблицею вендорів.
func firstTableRow(text string) string {
	for line := range strings.Lines(text) {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "|") {
			return line
		}
	}
	return ""
}

// --- Вузли extract_entities / index_vector / index_graph ---------------------

// fixtureBatch is the docling fixture after chunk + extract_entities.
func fixtureBatch(t *testing.T) Batch {
	t.Helper()
	data, err := os.ReadFile(fixtureDocMD)
	if err != nil {
		t.Fatal(err)
	}
	b, err := chunk(newNodeCtx(), newDocument("ledgerworks_soc2", fixtureDocMD, string(data)))
	if err != nil {
		t.Fatal(err)
	}
	b, err = extractEntities(newNodeCtx(), b)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestExtractEntities(t *testing.T) {
	t.Parallel()
	b := fixtureBatch(t)
	tableIDs := map[string]bool{}
	for _, c := range b.Chunks {
		if c.Kind == "table" {
			tableIDs[c.ID] = true
		}
	}
	tests := []struct {
		id, typ string
	}{
		{"vendor:acme-bank-jsc", "Vendor"},
		{"finding:f-101", "Finding"},
		{"severity:high", "Severity"},
		{"report:soc2-type-ii", "Report"},
		{"merchant:a-114", "Merchant"},
		{"contract:ct-2025-014", "Contract"},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			t.Parallel()
			for _, e := range b.Entities {
				if e.ID == tt.id {
					if e.Type != tt.typ {
						t.Errorf("type = %q, want %q", e.Type, tt.typ)
					}
					if !tableIDs[e.SourceChunkID] {
						t.Errorf("source chunk %q is not a table chunk", e.SourceChunkID)
					}
					return
				}
			}
			t.Errorf("entity %s not found", tt.id)
		})
	}
}

func TestExtractEntities_TextChunksGiveNothing(t *testing.T) {
	t.Parallel()
	in := Batch{Chunks: []Chunk{{ID: "c1", ParentID: "p1", Kind: "text", Text: "Acme Bank JSC"}}}
	got, err := extractEntities(newNodeCtx(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entities) != 0 {
		t.Errorf("entities from text chunk: %+v", got.Entities)
	}
}

func TestEntityID(t *testing.T) {
	t.Parallel()
	tests := []struct{ typ, name, want string }{
		{"Vendor", "Acme Bank JSC", "vendor:acme-bank-jsc"},
		{"Subcategory", "GOVERN 1.1", "subcategory:govern-1.1"},
		{"Merchant", " A-114 ", "merchant:a-114"},
	}
	for _, tt := range tests {
		if got := entityID(tt.typ, tt.name); got != tt.want {
			t.Errorf("entityID(%q, %q) = %q, want %q", tt.typ, tt.name, got, tt.want)
		}
	}
}

func TestEntityName_NISTCell(t *testing.T) {
	t.Parallel()
	tests := []struct{ typ, cell, want string }{
		{"Subcategory", "GOVERN 1.1: Legal and regulatory requirements involving AI are understood.", "GOVERN 1.1"},
		{"Category", "MAP 2: Categorization of the AI system is performed.", "MAP 2"},
		{"Category", "", ""},
		{"Vendor", " Acme Bank JSC ", "Acme Bank JSC"},
	}
	for _, tt := range tests {
		if got := entityName(tt.typ, tt.cell); got != tt.want {
			t.Errorf("entityName(%q, %q) = %q, want %q", tt.typ, tt.cell, got, tt.want)
		}
	}
}

func TestIndexVector(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		chunks []Chunk
		want   int
	}{
		{"parents are skipped", []Chunk{{ID: "p1", Text: "1 A"}, {ID: "p1.c1", ParentID: "p1", Text: "x"}}, 1},
		{"empty batch", nil, 0},
		{"two children", []Chunk{{ID: "p1"}, {ID: "p1.c1", ParentID: "p1", Text: "a"}, {ID: "p1.c2", ParentID: "p1", Text: "b"}}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := NewStores()
			got, err := newIndexVector(st.Vector)(newNodeCtx(), Batch{Chunks: tt.chunks})
			if err != nil {
				t.Fatal(err)
			}
			if got.VectorsAdded != tt.want || st.Vector.Len() != tt.want {
				t.Errorf("VectorsAdded=%d Len=%d, want %d", got.VectorsAdded, st.Vector.Len(), tt.want)
			}
		})
	}
}

func TestIndexVector_DuplicateIDIsError(t *testing.T) {
	t.Parallel()
	in := Batch{Chunks: []Chunk{{ID: "c", ParentID: "p", Text: "a"}, {ID: "c", ParentID: "p", Text: "b"}}}
	if _, err := newIndexVector(NewStores().Vector)(newNodeCtx(), in); err == nil {
		t.Fatal("same child indexed twice must be an error")
	}
}

func TestIndexGraph(t *testing.T) {
	t.Parallel()
	st := NewStores()
	b, err := newIndexGraph(st.Graph)(newNodeCtx(), fixtureBatch(t))
	if err != nil {
		t.Fatal(err)
	}
	if b.NodesAdded == 0 || b.NodesAdded > len(b.Entities) {
		t.Errorf("NodesAdded = %d, entities = %d", b.NodesAdded, len(b.Entities))
	}
	if nodes, edges := st.Graph.Stats(); nodes != b.NodesAdded || edges != b.EdgesAdded {
		t.Errorf("graph stats %d/%d, batch %d/%d", nodes, edges, b.NodesAdded, b.EdgesAdded)
	}
	// Acme appears in two tables: one node, two sources
	acme, ok := st.Graph.Node("vendor:acme-bank-jsc")
	if !ok || len(acme.Sources) != 2 {
		t.Errorf("acme node = %+v, want 2 sources", acme)
	}
}

// The compliance query from the homework: High findings of Acme in SOC2 Type II.
func TestIndexGraph_AcmeHighFindings(t *testing.T) {
	t.Parallel()
	st := NewStores()
	if _, err := newIndexGraph(st.Graph)(newNodeCtx(), fixtureBatch(t)); err != nil {
		t.Fatal(err)
	}
	got := highFindings(st.Graph, "vendor:acme-bank-jsc", "report:soc2-type-ii")
	want := []string{"F-101", "F-104"}
	if !slices.Equal(got, want) {
		t.Errorf("high findings = %v, want %v", got, want)
	}
	if got := highFindings(st.Graph, "vendor:globex-payments", "report:soc2-type-ii"); len(got) != 0 {
		t.Errorf("globex has SOC2 Type I, got %v", got)
	}
}

func TestIndexGraph_EntityWithoutSourceIsError(t *testing.T) {
	t.Parallel()
	in := Batch{Entities: []Entity{{ID: "vendor:x", Type: "Vendor", Name: "X"}}}
	if _, err := newIndexGraph(NewStores().Graph)(newNodeCtx(), in); err == nil {
		t.Fatal("entity without provenance must be an error")
	}
}

// --- Вузол report ------------------------------------------------------------

func TestReport(t *testing.T) {
	t.Parallel()
	doc := newDocument("d", "", "## 1 A\n\nтекст\n\n| H |\n|---|\n| v |\n")
	tests := []struct {
		name     string
		batch    Batch
		contains []string
	}{
		{"empty input", Batch{}, []string{"Parents: 0, Children: 0", "Tables: 0/0 preserved"}},
		{"table kept", Batch{Document: doc, Chunks: []Chunk{
			{ID: "p1", Text: "1 A"},
			{ID: "p1.c1", ParentID: "p1", Kind: "text", Text: "текст"},
			{ID: "p1.c2", ParentID: "p1", Kind: "table", Text: "| H |\n|---|\n| v |"},
		}, VectorsAdded: 2}, []string{"Parents: 1, Children: 2 (tables: 1)", "Tables: 1/1 preserved", "vectors_added: 2", "1 tables, 1 headings"}},
		{"table lost", Batch{Document: doc, Chunks: []Chunk{
			{ID: "p1", Text: "1 A"},
			{ID: "p1.c1", ParentID: "p1", Kind: "table", Text: "| H |\n|---|"},
		}}, []string{"Tables: 0/1 preserved"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := report(newNodeCtx(), tt.batch)
			if err != nil {
				t.Fatalf("report() error = %v", err)
			}
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("report() = %q; want substring %q", got, want)
				}
			}
		})
	}
}

func TestReport_SavesChunks(t *testing.T) {
	out := filepath.Join(t.TempDir(), "chunks.json")
	t.Setenv("CHUNKS_OUT", out)
	in := Batch{Chunks: []Chunk{{ID: "p1", DocumentID: "d", Kind: "heading", Text: "1 A"}}}
	if _, err := report(newNodeCtx(), in); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var got []Chunk
	if err := json.Unmarshal(data, &got); err != nil || len(got) != 1 || got[0].ID != "p1" {
		t.Errorf("saved chunks = %s, %v", data, err)
	}
}

// --- Граф цілком ------------------------------------------------------------

// TestGraph_EarlyWin проганяє весь граф так само, як `go run . console`, але
// без launcher-а: ранній win із Homework, відтворюваний у CI.
func TestGraph_EarlyWin(t *testing.T) {
	t.Parallel()
	a, err := newGraph(fakeConverter{}, NewStores())
	if err != nil {
		t.Fatalf("newGraph: %v", err)
	}
	res, err := labrun.Run(context.Background(), a, fixtureMD)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(res.Final, "Tables: 3/3 preserved") {
		t.Errorf("фінальна відповідь = %q; очікували рядок вузла report", res.Final)
	}
	// 7 вузлів після Start — 7 подій: жоден вузол не випав із ланцюжка.
	if got := len(res.Events); got != 7 {
		t.Errorf("подій = %d, очікували 7 (load … report)", got)
	}
}

// --- Контракт із ДЗ 6 --------------------------------------------------------

// TestChunkJSONContract: labs6 читає ваші чанки як JSON. Імена полів — це
// контракт між ДЗ 5 і ДЗ 6; якщо тест упав, ви перейменували поле.
func TestChunkJSONContract(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(Chunk{ID: "c", DocumentID: "d", ParentID: "p", Level: 2, Kind: "table", SectionPath: "s", Text: "t"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"id"`, `"document_id"`, `"parent_id"`, `"level"`, `"kind"`, `"section_path"`, `"text"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("JSON чанка без поля %s: %s", key, raw)
		}
	}
}
