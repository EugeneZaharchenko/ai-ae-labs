package corpus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name    string
		path    string
		wantN   int
		wantErr string
	}{
		{name: "lab corpus", path: filepath.Join("..", "..", "testdata", "chunks.json"), wantN: 21},
		{name: "missing file", path: filepath.Join(dir, "ghost.json"), wantErr: "ghost.json"},
		{name: "bad json", path: write("bad.json", "{"), wantErr: "bad.json"},
		{name: "empty", path: write("empty.json", "[]"), wantErr: "no chunks"},
		{name: "no id", path: write("noid.json", `[{"document_id":"d","text":"t"}]`), wantErr: "has no id"},
		{name: "no document", path: write("nodoc.json", `[{"id":"c","text":"t"}]`), wantErr: "no document_id"},
		{name: "no text", path: write("notext.json", `[{"id":"c","document_id":"d"}]`), wantErr: "no text"},
		{name: "duplicate", path: write("dup.json", `[{"id":"c","document_id":"d","text":"t"},{"id":"c","document_id":"d","text":"u"}]`), wantErr: "duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Load(tt.path)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if len(got) != tt.wantN {
				t.Errorf("chunks = %d, want %d", len(got), tt.wantN)
			}
		})
	}
}
