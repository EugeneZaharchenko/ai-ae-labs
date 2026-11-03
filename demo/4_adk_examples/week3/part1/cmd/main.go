// Agent for Week 3, Part 1: OntologyAwareKnowledgeBuilder
// Lossless PDF parser + ontology-driven chunker + entity resolver.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/internal/adkrun"
)

// Chunk represents a parsed document chunk with ontology metadata.
type Chunk struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Content  string `json:"content"`
	Type     string `json:"type"` // "text", "table", "heading"
	PageNum  int    `json:"page_num"`
	Entity   string `json:"entity,omitempty"`
}

// Document represents a parsed document.
type Document struct {
	Title  string  `json:"title"`
	Chunks []Chunk `json:"chunks"`
}

// parseDocument simulates lossless PDF parsing.
func parseDocument(ctx agent.Context, input string) (Document, error) {
	lines := strings.Split(input, "\n")
	if len(lines) < 2 {
		return Document{}, fmt.Errorf("expected at least title + content lines")
	}
	title := strings.TrimSpace(lines[0])
	doc := Document{Title: title}

	for i, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		chunkType := "text"
		if strings.HasPrefix(line, "# ") {
			chunkType = "heading"
		} else if strings.HasPrefix(line, "|") {
			chunkType = "table"
		}
		doc.Chunks = append(doc.Chunks, Chunk{
			ID:      fmt.Sprintf("chunk-%d", i),
			Content: line,
			Type:    chunkType,
			PageNum: i/5 + 1,
		})
	}
	return doc, nil
}

// resolveEntities performs entity resolution on chunks.
func resolveEntities(ctx agent.Context, doc Document) (Document, error) {
	seen := make(map[string]string)
	for i, chunk := range doc.Chunks {
		for _, word := range strings.Fields(chunk.Content) {
			clean := strings.Trim(word, ".,;:!?\"'()[]{}")
			if len(clean) > 3 && isUpper(clean[:1]) {
				if existing, ok := seen[clean]; ok {
					doc.Chunks[i].Entity = existing
				} else {
					seen[clean] = clean
					doc.Chunks[i].Entity = clean
				}
			}
		}
	}
	return doc, nil
}

func isUpper(s string) bool {
	return s[0] >= 'A' && s[0] <= 'Z'
}

// formatDocument formats the document as JSON.
func formatDocument(ctx agent.Context, doc Document) (string, error) {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	return string(b), nil
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("knowledge-builder", flag.ContinueOnError)
	input := fs.String("input", "# Q3 Report\n| Revenue | $1.2M |\nAcme Bank signed contract.\nACME Bank JSC renewed.", "document text")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	a, err := adkrun.Pipeline(
		"OntologyAwareKnowledgeBuilder",
		"Lossless parser + ontology-driven chunker + entity resolver",
		workflow.NewFunctionNode[string, Document]("parse", parseDocument, adkrun.NodeConfig()),
		workflow.NewFunctionNode[Document, Document]("resolve", resolveEntities, adkrun.NodeConfig()),
		workflow.NewFunctionNode[Document, string]("format", formatDocument, adkrun.NodeConfig()),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	out, err := adkrun.Run(context.Background(), a, *input, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Println(out)
	return 0
}
