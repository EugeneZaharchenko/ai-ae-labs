package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/chunkeval"
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
	EdgesAdded   int      `json:"edges_added"`
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
// далі mdblocks розкладає Markdown на блоки. Що таблиці цілі, видно в report
// (Tables: K/K preserved).
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

// newIndexVector adds every child to the vector index. Parents are not
// indexed: after a search they are loaded by ParentID.
// The embedding is the deterministic stub from internal/vecindex.
func newIndexVector(idx *vecindex.Index) func(agent.Context, Batch) (Batch, error) {
	return func(_ agent.Context, b Batch) (Batch, error) {
		for _, c := range b.Chunks {
			if c.ParentID == "" {
				continue
			}
			if err := idx.Add(c.ID, c.Text); err != nil {
				return Batch{}, fmt.Errorf("index_vector: %w", err)
			}
			b.VectorsAdded++
		}
		return b, nil
	}
}

// newIndexGraph puts entities into the graph and links entities from the same
// table row (Vendor -has_finding-> Finding). A repeated entity only adds its
// source chunk to the existing node, so NodesAdded counts unique nodes.
func newIndexGraph(g *kgraph.Graph) func(agent.Context, Batch) (Batch, error) {
	return func(_ agent.Context, b Batch) (Batch, error) {
		for _, e := range b.Entities {
			if e.SourceChunkID == "" {
				return Batch{}, fmt.Errorf("index_graph: entity %q without source chunk", e.ID)
			}
			added, err := g.AddNode(kgraph.Node{ID: e.ID, Type: e.Type, Name: e.Name, Sources: []string{e.SourceChunkID}})
			if err != nil {
				return Batch{}, fmt.Errorf("index_graph: %w", err)
			}
			if added {
				b.NodesAdded++
			}
		}
		for _, c := range b.Chunks {
			for _, row := range rowEntities(c) {
				for _, from := range row {
					for _, to := range row {
						rel, ok := relations[[2]string{from.Type, to.Type}]
						if !ok {
							continue
						}
						added, err := g.AddEdge(kgraph.Edge{From: from.ID, Rel: rel, To: to.ID, Source: c.ID})
						if err != nil {
							return Batch{}, fmt.Errorf("index_graph: %w", err)
						}
						if added {
							b.EdgesAdded++
						}
					}
				}
			}
		}
		return b, nil
	}
}

// naiveWindow is the size of the naive baseline from the lecture (800 tokens).
const naiveWindow = 800

// report prints the statistics that prove the chunking is lossless.
// If CHUNKS_OUT is set, the chunks are also saved as JSON for HW6.
func report(_ agent.Context, b Batch) (string, error) {
	var parents, children, tableChunks []string
	for _, c := range b.Chunks {
		if c.ParentID == "" {
			parents = append(parents, c.Text)
			continue
		}
		children = append(children, c.Text)
		if c.Kind == string(mdblocks.KindTable) {
			tableChunks = append(tableChunks, c.Text)
		}
	}
	var tables []string
	for _, t := range mdblocks.Tables(b.Document.Blocks) {
		tables = append(tables, t.Text)
	}
	blocks := b.Document.Blocks
	naive := chunkeval.NaiveWindows(b.Document.Markdown, naiveWindow, 0)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Blocks: %d (%d tables, %d headings, %d paragraphs, %d lists)\n",
		len(blocks), len(tables), len(mdblocks.Headings(blocks)), countKind(blocks, mdblocks.KindText), countKind(blocks, mdblocks.KindList))
	fmt.Fprintf(&sb, "Parents: %d, Children: %d (tables: %d)\n", len(parents), len(children), len(tableChunks))
	fmt.Fprintf(&sb, "Children size: %s\n", chunkeval.Measure(children))
	fmt.Fprintf(&sb, "Tables: %d/%d preserved\n", chunkeval.Preserved(tables, tableChunks), len(tables))
	fmt.Fprintf(&sb, "Naive %d-token windows: %d chunks, tables whole: %d/%d\n",
		naiveWindow, len(naive), chunkeval.Preserved(tables, naive), len(tables))
	fmt.Fprintf(&sb, "Entities: %d, vectors_added: %d, nodes_added: %d, edges_added: %d",
		len(b.Entities), b.VectorsAdded, b.NodesAdded, b.EdgesAdded)

	if out := os.Getenv("CHUNKS_OUT"); out != "" {
		if err := saveChunks(out, b.Chunks); err != nil {
			return "", err
		}
		fmt.Fprintf(&sb, "\nChunks saved to %s", out)
	}
	return sb.String(), nil
}

func countKind(blocks []mdblocks.Block, k mdblocks.Kind) int {
	n := 0
	for _, b := range blocks {
		if b.Kind == k {
			n++
		}
	}
	return n
}

// saveChunks writes the chunks as a JSON array — the format HW6 reads.
func saveChunks(path string, chunks []Chunk) error {
	data, err := json.MarshalIndent(chunks, "", "  ")
	if err != nil {
		return fmt.Errorf("save chunks: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("save chunks: %w", err)
	}
	return nil
}
