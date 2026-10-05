// Package corpus reads the chunk index that Lab 5 produces.
//
// The JSON shape is the contract between the two labs: labs5's Chunk marshals
// to exactly these fields (labs5/main_test.go: TestChunkJSONContract). Without
// a finished Lab 5, testdata/chunks.json in labs6 is a ready corpus built from
// the same LEDGERWORKS document.
//
// It lives outside the lab package because the student's job is retrieval,
// re-ranking and caching, not reading a file.
package corpus

import (
	"encoding/json"
	"fmt"
	"os"
)

// Chunk is one retrievable unit with its provenance.
type Chunk struct {
	ID          string `json:"id"`
	DocumentID  string `json:"document_id"`
	ParentID    string `json:"parent_id,omitempty"`
	Level       int    `json:"level"`
	Kind        string `json:"kind"`
	SectionPath string `json:"section_path,omitempty"`
	Text        string `json:"text"`
}

// Load reads a JSON array of chunks. Every chunk needs an id, a document_id
// and text: a chunk without them cannot be cited, and an answer that cannot
// cite its source does not count in this course.
func Load(path string) ([]Chunk, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("corpus: %w", err)
	}
	var chunks []Chunk
	if err := json.Unmarshal(data, &chunks); err != nil {
		return nil, fmt.Errorf("corpus: %s: %w", path, err)
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("corpus: %s: no chunks", path)
	}
	seen := make(map[string]bool, len(chunks))
	for i, c := range chunks {
		switch {
		case c.ID == "":
			return nil, fmt.Errorf("corpus: %s: chunk #%d has no id", path, i)
		case c.DocumentID == "":
			return nil, fmt.Errorf("corpus: %s: chunk %q has no document_id", path, c.ID)
		case c.Text == "":
			return nil, fmt.Errorf("corpus: %s: chunk %q has no text", path, c.ID)
		case seen[c.ID]:
			return nil, fmt.Errorf("corpus: %s: duplicate chunk id %q", path, c.ID)
		}
		seen[c.ID] = true
	}
	return chunks, nil
}
