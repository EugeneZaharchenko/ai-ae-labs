package ledgerkb

import (
	"errors"
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// Tool names. The strategy's `## Tools` allowlist is made of these.
const (
	ToolSearch = "kb_search"
	ToolRead   = "kb_read"
)

// SearchInput is the kb_search contract.
type SearchInput struct {
	Query string `json:"query" jsonschema:"Keywords to look for in LEDGERWORKS policy documents"`
}

// SearchOutput lists the best matches; Snippet is only the document's scope paragraph.
type SearchOutput struct {
	Hits []Hit  `json:"hits"`
	Note string `json:"note,omitempty"`
}

// ReadInput is the kb_read contract.
type ReadInput struct {
	ID string `json:"id" jsonschema:"Document id from kb_search, for example REF-001"`
}

// ReadOutput is a whole document.
type ReadOutput struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Tool builds one of the KB tools by name. An unknown name is an error, so a
// strategy cannot grant the worker a tool this package does not implement.
func (kb *KB) Tool(name string) (tool.Tool, error) {
	switch name {
	case ToolSearch:
		return functiontool.New(functiontool.Config{
			Name:        ToolSearch,
			Description: "Searches LEDGERWORKS policy documents by keyword. Returns up to 3 hits with only the head of each document; use kb_read for the full text.",
		}, func(_ agent.Context, in SearchInput) (SearchOutput, error) {
			if in.Query == "" {
				return SearchOutput{}, errors.New("query is empty")
			}
			hits := kb.Search(in.Query, 3)
			out := SearchOutput{Hits: hits}
			if len(hits) == 0 {
				out.Note = "no document matched; try different keywords"
			}
			return out, nil
		})
	case ToolRead:
		return functiontool.New(functiontool.Config{
			Name:        ToolRead,
			Description: "Returns the full text of one LEDGERWORKS policy document by id.",
		}, func(_ agent.Context, in ReadInput) (ReadOutput, error) {
			d, ok := kb.Get(in.ID)
			if !ok {
				return ReadOutput{}, fmt.Errorf("no document with id %q; ids come from kb_search", in.ID)
			}
			return ReadOutput{ID: d.ID, Title: d.Title, Body: d.Body}, nil
		})
	}
	return nil, fmt.Errorf("ledgerkb: unknown tool %q", name)
}

// Tools builds the named tools in order.
func (kb *KB) Tools(names []string) ([]tool.Tool, error) {
	out := make([]tool.Tool, 0, len(names))
	for _, n := range names {
		t, err := kb.Tool(n)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// AllToolNames is the allowlist a runtime would hand to ParseStrategy.
func AllToolNames() []string { return []string{ToolSearch, ToolRead} }
