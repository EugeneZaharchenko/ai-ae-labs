// Скелет тестів для ДЗ 5.
//
// Homework.md, пункт 7, вимагає табличні тести на кожен вузол графа через
// agent.StrictContextMock — і `go test ./...` зеленим. Цей файл дає готову
// форму, щоб ви не витрачали перші 40 хвилин на «як узагалі підсунути
// agent.Context у функцію вузла».
//
// ЯК ЦИМ КОРИСТУВАТИСЬ
//
//	go test ./...            — зараз зелено: тести перевіряють ЗАГЛУШКИ
//	                           (один гігантський чанк — це поточна поведінка).
//	                           Це навмисно: зелений старт означає, що
//	                           середовище живе, і далі ви ламаєте тести
//	                           свідомо, а не воюєте з інструментами.
//
// Ваш шлях: реалізуєте TODO у pipeline.go → тести нижче ПАДАЮТЬ, бо вони
// описують стару поведінку → переписуєте їх під нову. Місця, які треба
// переписати, позначені TODO(студент). Тест «таблиця не ріжеться»
// (TestChunk_TableStaysWhole) уже описує ЦІЛЬОВУ поведінку і має падати, доки
// chunk — заглушка: це контракт ДЗ, а не заготовка. Він єдиний тут із t.Skip —
// зніміть skip, коли візьметесь за chunking.
//
// Тестам не потрібні ні Python, ні docling: PDF-шлях перевіряється фейковим
// конвертером (fakeConverter). Справжній docling-mcp перевіряє opt-in тест
// internal/docling (DOCLING_E2E=1), а його реальний вихід для тестового PDF
// збережено в testdata/ledgerworks_soc2.docling.md.
//
// Чому StrictContextMock, а не nil: вузли приймають agent.Context. Передати
// туди nil можна лише доти, доки ви його не використовуєте; щойно у вашому
// коді з'явиться ctx.InvocationID() або емісія події, nil дасть panic у
// найгіршому місці — у проді. StrictContextMock панікує ГУЧНО і одразу на
// будь-якому методі, про який ви не подумали, тож помилка знаходиться в тесті,
// а не на демо.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
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

// TestChunk_Stub фіксує ПОТОЧНУ поведінку заглушки: один чанк на весь документ.
//
// TODO(студент): щойно ви реалізуєте Parent-Child chunking, цей тест має
// впасти — і це правильно. Замініть його на перевірку вашої ієрархії:
// скільки parents, скільки children, чи в кожного child заповнений ParentID
// і SectionPath, чи Level зростає всередині розділу.
func TestChunk_Stub(t *testing.T) {
	t.Parallel()
	got, err := chunk(newNodeCtx(), newDocument("d1", "", "# Розділ\n\nабзац\n"))
	if err != nil {
		t.Fatalf("chunk() error = %v", err)
	}
	if len(got.Chunks) != 1 {
		t.Fatalf("заглушка має повертати 1 чанк, отримали %d — схоже, ви вже реалізували chunking; перепишіть цей тест", len(got.Chunks))
	}
	if got.Chunks[0].ParentID != "" {
		t.Errorf("ParentID кореневого чанка = %q, очікували порожній", got.Chunks[0].ParentID)
	}
	if got.Chunks[0].DocumentID != "d1" {
		t.Errorf("DocumentID = %q, очікували d1 — provenance втрачено", got.Chunks[0].DocumentID)
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
//
// TODO(студент): приберіть t.Skip, коли візьметесь за вузол chunk.
func TestChunk_TableStaysWhole(t *testing.T) {
	t.Skip("зніміть skip, коли реалізуєте Parent-Child chunking у pipeline.go")

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

// TestStubNodesPassThrough фіксує заглушки: вузли повертають batch без змін.
//
// TODO(студент): замініть на табличні тести ваших вузлів — наприклад,
// extract_entities знаходить Vendor "Acme Bank JSC" із SourceChunkID;
// index_vector дає VectorsAdded == кількість children і st.Vector.Len() те
// саме; index_graph не створює дублікатів (NodesAdded ≤ len(Entities)).
func TestStubNodesPassThrough(t *testing.T) {
	t.Parallel()
	in := Batch{Chunks: []Chunk{{ID: "c1", Text: "Acme Bank JSC"}}}
	st := NewStores()
	nodes := map[string]func(agent.Context, Batch) (Batch, error){
		"extract_entities": extractEntities,
		"index_vector":     newIndexVector(st.Vector),
		"index_graph":      newIndexGraph(st.Graph),
	}
	for name, node := range nodes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := node(newNodeCtx(), in)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if len(got.Chunks) != len(in.Chunks) {
				t.Errorf("%s загубив чанки: %d → %d", name, len(in.Chunks), len(got.Chunks))
			}
		})
	}
}

// --- Вузол report ------------------------------------------------------------

// TODO(студент): за Homework п.5 report має друкувати статистику за рівнями,
// розміри (min/avg/max) і `Tables: K/K preserved`. Розширте таблицю кейсів,
// коли реалізуєте це.
func TestReport(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		chunks   []Chunk
		contains string
	}{
		{name: "порожній вхід", chunks: nil, contains: "0"},
		{name: "один чанк", chunks: []Chunk{{ID: "c1"}}, contains: "1"},
		{name: "три чанки", chunks: []Chunk{{ID: "c1"}, {ID: "c2"}, {ID: "c3"}}, contains: "3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := report(newNodeCtx(), Batch{Chunks: tt.chunks})
			if err != nil {
				t.Fatalf("report() error = %v", err)
			}
			if !strings.Contains(got, tt.contains) {
				t.Errorf("report() = %q; очікували підрядок %q", got, tt.contains)
			}
		})
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
	if !strings.Contains(res.Final, "Отримано 1 чанків") {
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
