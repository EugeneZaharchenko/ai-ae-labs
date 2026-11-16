// Package sandbox is brake (a) of the self-improvement loop: the bounded
// surface of change. The agent may write exactly two things inside its root:
// the strategy file and files under experiments/ (where discarded candidates
// are kept). Everything else — code, data, eval sets, the audit log — is
// read-only, and a write outside the surface is refused, not "discouraged".
//
// A prefix check at the call site would miss three real cases: `..` traversal,
// an absolute (or, on Windows, rooted) path, and a symlink pointing outside.
// They are all handled here, once, where they can be tested.
//
// This is a check inside the process, not isolation: the real boundary for
// untrusted code is a sandbox one level lower (gVisor / microVM).
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StrategyFile is the one strategy artifact the agent may rewrite.
const StrategyFile = "strategy.md"

// ExperimentsDir holds discarded candidates; the agent may write files in it.
const ExperimentsDir = "experiments"

// ErrNotMutable reports a write outside the surface of change.
var ErrNotMutable = errors.New("sandbox: шлях поза поверхнею змін")

// IsMutable reports whether the agent may write path inside root. Only
// strategy.md and files under experiments/ are mutable.
func IsMutable(root, path string) bool {
	_, err := Resolve(root, path)
	return err == nil
}

// Resolve checks path against the surface of change and returns its absolute
// form, or an error wrapping ErrNotMutable.
func Resolve(root, path string) (string, error) {
	rel, err := relative(path)
	if err != nil {
		return "", err
	}
	if !allowed(rel) {
		return "", fmt.Errorf("%w: %q; дозволено лише %s і %s/", ErrNotMutable, path, StrategyFile, ExperimentsDir)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("sandbox: root: %w", err)
	}
	if r, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = r
	}
	full := filepath.Join(absRoot, rel)
	if err := noSymlinks(absRoot, rel); err != nil {
		return "", err
	}
	return full, nil
}

// relative cleans path and rejects every form that could leave the root.
func relative(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%w: порожній шлях", ErrNotMutable)
	}
	// A leading slash or backslash is "rooted" on Windows even without a drive
	// letter, and filepath.IsAbs says false for it there. Reject both forms.
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" ||
		strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) {
		return "", fmt.Errorf("%w: %q — абсолютний шлях", ErrNotMutable, path)
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q виходить за межі кореня", ErrNotMutable, path)
	}
	return clean, nil
}

// allowed is the whole policy: the exact strategy file, or a file inside
// experiments/ (not the directory itself).
func allowed(rel string) bool {
	if rel == StrategyFile {
		return true
	}
	return strings.HasPrefix(rel, ExperimentsDir+string(filepath.Separator)) &&
		len(rel) > len(ExperimentsDir)+1
}

// noSymlinks refuses a path whose existing components are symlinks: a
// symlink named strategy.md turns an allowed write into a write anywhere.
func noSymlinks(root, rel string) error {
	cur := root
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("sandbox: %w", err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %q — символьне посилання", ErrNotMutable, rel)
		}
	}
	return nil
}

// WriteFile writes content to path after Resolve, atomically: a crash in the
// middle leaves the old content or the new one, never half of it.
func WriteFile(root, path string, content []byte) error {
	full, err := Resolve(root, path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("sandbox: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), filepath.Base(full)+".tmp-*")
	if err != nil {
		return fmt.Errorf("sandbox: temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("sandbox: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("sandbox: close: %w", err)
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		return fmt.Errorf("sandbox: rename: %w", err)
	}
	return nil
}

// ReadFile reads a file through the same check, so the loop never reads a
// strategy that a symlink swapped in.
func ReadFile(root, path string) ([]byte, error) {
	full, err := Resolve(root, path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(full)
}
