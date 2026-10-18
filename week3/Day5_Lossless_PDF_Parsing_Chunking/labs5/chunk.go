package main

import (
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/chunkeval"
	"github.com/dimetron/ai-eng-course/labs/week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/mdblocks"
)

// maxChildTokens is the limit for text and list children (chunkeval.Tokens).
// Tables ignore it: one big whole table is better than two halves.
const maxChildTokens = 200

// chunk builds parent-child chunks from doc.Blocks.
//
// Parents are top-level sections: a heading with Depth 1 ("2 Findings") or an
// unnumbered heading. Depth > 1 ("2.1 ...") does not open a new parent, it only
// extends the section path, because docling prints every heading as "##" and
// the real level is only in the number. Every other block becomes a child of
// the current parent; a table is always exactly one child.
func chunk(_ agent.Context, doc Document) (Batch, error) {
	if strings.TrimSpace(doc.Markdown) == "" {
		return Batch{}, fmt.Errorf("порожній документ")
	}
	c := chunker{doc: doc}
	for _, b := range doc.Blocks {
		if b.Kind == mdblocks.KindHeading {
			c.heading(b)
			continue
		}
		c.child(b)
	}
	return Batch{Document: doc, Chunks: c.out}, nil
}

type chunker struct {
	doc      Document
	out      []Chunk
	parentID string
	path     []string // heading titles from the top section down
	parents  int
	children int // children of the current parent, for ids
}

func (c *chunker) heading(b mdblocks.Block) {
	if b.Depth > 1 && c.parentID != "" {
		// keep the first depth-1 titles, replace the rest
		keep := min(b.Depth-1, len(c.path))
		c.path = append(c.path[:keep:keep], b.Title)
		return
	}
	c.newParent(b.Title)
}

func (c *chunker) newParent(title string) {
	c.parents++
	c.children = 0
	c.parentID = fmt.Sprintf("p%d", c.parents)
	c.path = []string{title}
	c.out = append(c.out, Chunk{
		ID:          c.parentID,
		DocumentID:  c.doc.ID,
		Level:       0,
		Kind:        string(mdblocks.KindHeading),
		SectionPath: title,
		Text:        title,
	})
}

func (c *chunker) child(b mdblocks.Block) {
	if c.parentID == "" {
		// text before the first heading still needs a parent
		c.newParent(c.doc.ID)
	}
	var parts []string
	switch b.Kind {
	case mdblocks.KindTable:
		parts = []string{b.Text} // never split a table
	case mdblocks.KindList:
		parts = pack(strings.Split(b.Text, "\n"), "\n")
	default:
		parts = pack(sentences(b.Text), " ")
	}
	for _, text := range parts {
		c.children++
		c.out = append(c.out, Chunk{
			ID:          fmt.Sprintf("%s.c%d", c.parentID, c.children),
			DocumentID:  c.doc.ID,
			ParentID:    c.parentID,
			Level:       2,
			Kind:        string(b.Kind),
			SectionPath: strings.Join(c.path, " / "),
			Text:        text,
		})
	}
}

// pack joins pieces into chunks of at most maxChildTokens. A single piece that
// is longer than the limit is cut by words.
func pack(pieces []string, sep string) []string {
	var out []string
	var cur []string
	size := 0
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, sep))
			cur, size = nil, 0
		}
	}
	for _, p := range pieces {
		p = strings.TrimSpace(p)
		n := chunkeval.Tokens(p)
		if n == 0 {
			continue
		}
		if n > maxChildTokens {
			flush()
			out = append(out, chunkeval.NaiveWindows(p, maxChildTokens, 0)...)
			continue
		}
		if size+n > maxChildTokens {
			flush()
		}
		cur = append(cur, p)
		size += n
	}
	flush()
	return out
}

// sentences splits text after words that end with . ! or ?
func sentences(text string) []string {
	var out []string
	var cur []string
	for _, w := range strings.Fields(text) {
		cur = append(cur, w)
		if strings.HasSuffix(w, ".") || strings.HasSuffix(w, "!") || strings.HasSuffix(w, "?") {
			out = append(out, strings.Join(cur, " "))
			cur = nil
		}
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, " "))
	}
	return out
}
