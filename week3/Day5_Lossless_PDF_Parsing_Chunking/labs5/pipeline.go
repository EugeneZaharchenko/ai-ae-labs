package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/kgraph"
	"github.com/dimetron/ai-eng-course/labs/week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/mdblocks"
	"github.com/dimetron/ai-eng-course/labs/week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/vecindex"
)

// Source — документ, який прийшов на вхід конвеєра.
type Source struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // "pdf" | "markdown"
}

// Document — документ після lossless-парсингу: увесь зміст у Markdown,
// таблиці — Markdown-таблицями із заголовками стовпчиків. Blocks — той самий
// Markdown, уже розкладений на заголовки / абзаци / списки / таблиці
// (internal/mdblocks). Саме з Blocks ви будуєте чанки.
type Document struct {
	ID       string           `json:"id"`
	Path     string           `json:"path"`
	Markdown string           `json:"markdown"`
	Blocks   []mdblocks.Block `json:"blocks"`
}

// Chunk — одиниця майбутньої бази знань Research Agent.
//
// Цей JSON-формат — контракт із ДЗ 6: labs6 читає саме такі чанки
// (див. labs6/testdata/chunks.json). Додавайте поля, але не перейменовуйте
// наявні — інакше ДЗ 6 не прочитає ваш індекс.
type Chunk struct {
	ID          string `json:"id"`
	DocumentID  string `json:"document_id"`
	ParentID    string `json:"parent_id,omitempty"`    // порожній для батьківських чанків
	Level       int    `json:"level"`                  // 0 = розділ, 1 = підрозділ, 2 = абзац/таблиця
	Kind        string `json:"kind"`                   // "heading" | "text" | "table" | "list"
	SectionPath string `json:"section_path,omitempty"` // напр. "2 Findings / 2.1 Security Findings"
	Text        string `json:"text"`
}

// Entity — сутність, витягнута з чанка (Vendor, Finding, Merchant, Contract…).
// SourceChunkID — provenance: без нього аудитор не знайде первинний фрагмент.
type Entity struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	Name          string `json:"name"`
	SourceChunkID string `json:"source_chunk_id"`
}

// Batch тече вузлами після chunk: кожен вузол додає свою частину.
type Batch struct {
	Document     Document `json:"document"`
	Chunks       []Chunk  `json:"chunks"`
	Entities     []Entity `json:"entities,omitempty"`
	VectorsAdded int      `json:"vectors_added"`
	NodesAdded   int      `json:"nodes_added"`
}

// MarkdownConverter перетворює PDF на Markdown. У main.go це docling-mcp
// (internal/docling); у тестах — фейк без мережі й без Python.
type MarkdownConverter interface {
	ToMarkdown(ctx context.Context, path string) (string, error)
}

// Stores — сховища, які наповнюють вузли index_*. Вони живуть поза графом:
// вузли лишаються функціями, а після прогону тест (або ваш A1) читає сховища
// напряму.
type Stores struct {
	Vector *vecindex.Index
	Graph  *kgraph.Graph
}

// NewStores повертає порожні in-memory сховища.
func NewStores() Stores {
	return Stores{Vector: vecindex.New(), Graph: kgraph.New()}
}

// load перевіряє вхід: шлях із повідомлення користувача → Source.
// Тип визначається за вмістом (magic bytes %PDF), а не лише за розширенням.
func load(_ agent.Context, path string) (Source, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Source{}, fmt.Errorf("порожній шлях: введіть шлях до PDF або Markdown")
	}
	f, err := os.Open(path)
	if err != nil {
		return Source{}, fmt.Errorf("не вдалося прочитати %q: %w", path, err)
	}
	defer f.Close()
	head := make([]byte, 5)
	n, _ := f.Read(head)
	if bytes.HasPrefix(head[:n], []byte("%PDF-")) {
		return Source{Path: path, Kind: "pdf"}, nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".txt":
		return Source{Path: path, Kind: "markdown"}, nil
	}
	return Source{}, fmt.Errorf("%q: непідтримуваний формат — очікуємо PDF або Markdown", path)
}

// newParse повертає вузол parse. PDF іде в docling-mcp, Markdown читається як є;
// далі mdblocks розкладає Markdown на блоки.
//
// TODO(студент): перевірте на своєму PDF, що кожна таблиця вийшла цілою
// Markdown-таблицею із заголовками (mdblocks.Tables(doc.Blocks) — швидкий
// лічильник). Сканований PDF без текстового шару потребує OCR — docling уміє,
// але це повільніше; зазначте в README.
func newParse(conv MarkdownConverter) func(agent.Context, Source) (Document, error) {
	return func(ctx agent.Context, src Source) (Document, error) {
		var md string
		switch src.Kind {
		case "pdf":
			out, err := conv.ToMarkdown(ctx, src.Path)
			if err != nil {
				return Document{}, fmt.Errorf("parse %q: %w", src.Path, err)
			}
			md = out
		case "markdown":
			data, err := os.ReadFile(src.Path)
			if err != nil {
				return Document{}, fmt.Errorf("parse %q: %w", src.Path, err)
			}
			md = string(data)
		default:
			return Document{}, fmt.Errorf("parse: невідомий тип %q", src.Kind)
		}
		if strings.TrimSpace(md) == "" {
			return Document{}, fmt.Errorf("parse %q: порожній документ", src.Path)
		}
		return newDocument(documentID(src.Path), src.Path, md), nil
	}
}

// newDocument — єдиний спосіб зібрати Document: Blocks завжди відповідають
// Markdown. У своїх тестах будуйте документи теж через нього.
func newDocument(id, path, md string) Document {
	return Document{ID: id, Path: path, Markdown: md, Blocks: mdblocks.Parse(md)}
}

// documentID — стабільний ідентифікатор документа з імені файла.
func documentID(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// chunk нарізає документ ієрархічно.
//
// TODO(студент): Parent-Child chunking — головна частина ДЗ. Ідіть по
// doc.Blocks (вони вже розібрані, див. internal/mdblocks):
//  1. KindHeading з Depth == 1 відкриває новий parent (Level 0, ParentID "").
//     Depth > 1 — підрозділ: оновіть SectionPath ("2 Findings / 2.1 …").
//     Depth == 0 — заголовок без номера (назва документа): теж parent.
//  2. Решта блоків — children (Level 2) поточного parent: ParentID,
//     SectionPath, DocumentID заповнені; Kind = string(block.Kind).
//  3. KindTable — РІВНО один child, Text = block.Text без змін. Не ріжте.
//  4. Довгий KindText (> maxChildTokens, див. chunkeval.Tokens) — розбийте
//     по реченнях на кілька children.
//  5. Блок до першого заголовка теж потребує parent — створіть неявний.
//
// Текст parent — його заголовок (як у labs6/testdata/chunks.json).
func chunk(_ agent.Context, doc Document) (Batch, error) {
	if strings.TrimSpace(doc.Markdown) == "" {
		return Batch{}, fmt.Errorf("порожній документ")
	}
	// заглушка: один гігантський чанк
	return Batch{Document: doc, Chunks: []Chunk{{ID: "c1", DocumentID: doc.ID, Level: 0, Kind: "text", Text: doc.Markdown}}}, nil
}

// maxChildTokens — стеля довжини текстового child (у chunkeval.Tokens).
// Таблиці на неї не зважають: краще великий цілий чанк, ніж дві половини.
const maxChildTokens = 200

// extractEntities витягує сутності з чанків.
//
// TODO(студент): читайте сутності з чанків Kind == "table" — там структура
// вже є. mdblocks.Parse(c.Text)[0].Table дає таблицю назад, а
// Table.Column("Vendor") — усі значення стовпчика. Типи на ваш вибір (Vendor,
// Finding, Merchant, Contract…); у кожної сутності — SourceChunkID = c.ID.
// ID сутності робіть стабільним: "vendor:acme-bank-jsc".
// Для ++ A2 — entity resolution поверх цього списку.
func extractEntities(_ agent.Context, b Batch) (Batch, error) {
	return b, nil // заглушка: сутностей немає
}

// newIndexVector повертає вузол index_vector над idx.
//
// TODO(студент): для кожного child (ParentID != "") — idx.Add(c.ID, c.Text)
// і VectorsAdded++. Parents не індексуйте: їх підвантажують за ParentID
// після пошуку. Ембединг — стаб (internal/vecindex), так і напишіть у README.
func newIndexVector(idx *vecindex.Index) func(agent.Context, Batch) (Batch, error) {
	_ = idx
	return func(_ agent.Context, b Batch) (Batch, error) {
		return b, nil // заглушка: нічого не індексуємо
	}
}

// newIndexGraph повертає вузол index_graph над g.
//
// TODO(студент): кожна Entity → g.AddNode(kgraph.Node{ID, Type, Name,
// Sources: []string{SourceChunkID}}); NodesAdded++ лише коли AddNode
// повернув true (повтор — це злиття provenance, не новий вузол). Ребра між
// сутностями одного рядка таблиці (Vendor —has_finding→ Finding) — g.AddEdge.
func newIndexGraph(g *kgraph.Graph) func(agent.Context, Batch) (Batch, error) {
	_ = g
	return func(_ agent.Context, b Batch) (Batch, error) {
		return b, nil // заглушка: граф порожній
	}
}

// report рахує статистику нарізки.
//
// TODO(студент): кількість чанків за рівнями (parents/children),
// min/avg/max у токенах, `Tables: K/K preserved`. Готові лічильники — у
// internal/chunkeval: Measure (min/avg/max), Preserved (скільки таблиць
// документа лежать цілими в чанках; таблиці документа —
// mdblocks.Tables(b.Document.Blocks)), NaiveWindows (baseline для README).
func report(_ agent.Context, b Batch) (string, error) {
	return fmt.Sprintf("Отримано %d чанків. TODO: статистика за рівнями і таблицями.", len(b.Chunks)), nil
}
