package main

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
)

func TestParseDocument(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	input := "# Q3 Report\nThis is a paragraph.\n| Table data |"
	doc, err := parseDocument(ctx, input)
	if err != nil {
		t.Fatalf("parseDocument failed: %v", err)
	}
	if doc.Title != "# Q3 Report" {
		t.Errorf("expected title '# Q3 Report', got %q", doc.Title)
	}
	if len(doc.Chunks) < 2 {
		t.Errorf("expected at least 2 chunks, got %d", len(doc.Chunks))
	}
}

func TestParseDocument_Invalid(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	_, err := parseDocument(ctx, "only-title")
	if err == nil {
		t.Fatal("expected error for single-line input")
	}
}

func TestParseDocument_ChunkTypes(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	input := "Doc\n# Heading\nParagraph\n| Table |"
	doc, err := parseDocument(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	types := make(map[string]bool)
	for _, c := range doc.Chunks {
		types[c.Type] = true
	}
	if !types["heading"] {
		t.Error("expected heading type")
	}
	if !types["table"] {
		t.Error("expected table type")
	}
	if !types["text"] {
		t.Error("expected text type")
	}
}

func TestResolveEntities(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	doc := Document{
		Title: "Test",
		Chunks: []Chunk{
			{ID: "c1", Content: "Acme Bank signed", Type: "text"},
			{ID: "c2", Content: "ACME Bank JSC renewed", Type: "text"},
		},
	}
	resolved, err := resolveEntities(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Chunks[0].Entity == "" {
		t.Error("expected entity resolution for 'Acme'")
	}
}

func TestFormatDocument(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	doc := Document{Title: "Test", Chunks: []Chunk{{ID: "c1", Content: "data", Type: "text", PageNum: 1}}}
	out, err := formatDocument(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Test") {
		t.Errorf("expected title in output, got: %s", out)
	}
}
