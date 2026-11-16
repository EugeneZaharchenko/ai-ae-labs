package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIsMutable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tests := []struct {
		path string
		want bool
	}{
		{"strategy.md", true},
		{"./strategy.md", true},
		{"experiments/exp-001.md", true},
		{"experiments/sub/exp.md", true},
		{"experiments", false},
		{"experiments/", false},
		{"experiments.log", false},
		{"Strategy.md", false},
		{"strategy.md.bak", false},
		{"main.go", false},
		{"eval/dev.json", false},
		{"internal/sandbox/sandbox.go", false},
		{"../strategy.md", false},
		{"experiments/../main.go", false},
		{"experiments/../../x", false},
		{"/abs/strategy.md", false},
		{`\rooted\strategy.md`, false},
		{"C:/x/strategy.md", false},
		{"", false},
		{".", false},
		{"..", false},
	}
	for _, tt := range tests {
		if got := IsMutable(root, tt.path); got != tt.want {
			t.Errorf("IsMutable(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestResolveRejectsSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "evil.md")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, StrategyFile)); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	if _, err := Resolve(root, StrategyFile); !errors.Is(err, ErrNotMutable) {
		t.Fatalf("Resolve through symlink = %v, want ErrNotMutable", err)
	}
}

func TestWriteAndReadFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := WriteFile(root, StrategyFile, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(root, "experiments/exp-001.md", []byte("discarded")); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(root, StrategyFile)
	if err != nil || string(got) != "v1" {
		t.Fatalf("ReadFile = %q, %v", got, err)
	}
	if err := WriteFile(root, "main.go", []byte("x")); !errors.Is(err, ErrNotMutable) {
		t.Fatalf("WriteFile(main.go) = %v, want ErrNotMutable", err)
	}
	if _, err := ReadFile(root, "eval/dev.json"); !errors.Is(err, ErrNotMutable) {
		t.Fatalf("ReadFile(eval) = %v, want ErrNotMutable", err)
	}
	if _, err := os.Stat(filepath.Join(root, "main.go")); !os.IsNotExist(err) {
		t.Fatal("main.go must not be created")
	}
}
