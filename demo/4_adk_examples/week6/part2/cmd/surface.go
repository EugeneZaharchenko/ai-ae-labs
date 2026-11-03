package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The bounded mutable surface: the agent may change exactly ONE declared file.
// Everything else in the working directory is out of reach, and an attempt to
// reach it is REJECTED, not discouraged.
//
// This is a type and not a strings.HasPrefix at the write site, because a prefix
// check catches the naive case and misses three real ones: `..` traversal, an
// absolute path, and a symlink inside the directory that points outside it. A
// guarantee that is easy to walk around is a comment, not a guarantee.
//
// It is process-level hygiene, not isolation: it constrains what THIS loop will
// write, not what generated code could do if it ran. Running agent-written code
// belongs in a sandbox (the platform track).

// ErrOutsideSurface marks a path outside the declared surface. A sentinel, so a
// caller and a test check it with errors.Is and the wording can change.
var ErrOutsideSurface = errors.New("path is outside the writable surface")

// Surface is one writable file inside one working directory.
type Surface struct {
	root string // absolute, symlink-resolved
	rel  string
	abs  string
}

// NewSurface declares the surface: a directory and exactly one file in it.
//
// root is resolved through symlinks first: on macOS t.TempDir() is under
// /var/folders, which is really /private/var/folders, and without resolving it
// every comparison would report a false "outside".
func NewSurface(root, rel string) (*Surface, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("surface: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("surface: root %q: %w", root, err)
	}
	clean := filepath.Clean(rel)
	if rel == "" || filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, "..") {
		return nil, fmt.Errorf("surface: %q is not a relative path inside root", rel)
	}
	return &Surface{root: resolved, rel: clean, abs: filepath.Join(resolved, clean)}, nil
}

// Path is the surface file's absolute path.
func (s *Surface) Path() string { return s.abs }

// Check reports whether rel names the surface file and nothing else.
func (s *Surface) Check(rel string) error {
	clean := filepath.Clean(rel)
	switch {
	case rel == "" || filepath.IsAbs(clean):
		return fmt.Errorf("%w: %q", ErrOutsideSurface, rel)
	case clean != s.rel:
		return fmt.Errorf("%w: %q (the only writable file is %q)", ErrOutsideSurface, rel, s.rel)
	}
	// The name is right; now make sure the file is not a symlink pointing out.
	if fi, err := os.Lstat(s.abs); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %q is a symlink", ErrOutsideSurface, rel)
	}
	return nil
}

// Write replaces the surface file's content after Check.
func (s *Surface) Write(rel, content string) error {
	if err := s.Check(rel); err != nil {
		return err
	}
	return os.WriteFile(s.abs, []byte(content), 0o644)
}

// Read returns the surface file's content.
func (s *Surface) Read() (string, error) {
	b, err := os.ReadFile(s.abs)
	return string(b), err
}
