// Package adkenv loads provider credentials from the repo's apps/.env file.
//
// Rationale for the course: labs must run key-free by default (§2b "no surprise
// prerequisites"), but the production-shaped path needs real credentials. This
// package makes the difference explicit — Load is best-effort, and every caller
// must still handle the "no key" case.
//
// Parsing is deliberately minimal: KEY=VALUE, # comments, blank lines, optional
// surrounding quotes. It is not a dotenv-spec implementation and does not do
// variable interpolation.
package adkenv

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound reports that no apps/.env file was found walking up from the
// starting directory.
var ErrNotFound = errors.New("adkenv: apps/.env not found")

// Parse reads KEY=VALUE pairs from r.
//
// Blank lines and lines whose first non-space character is '#' are skipped. A
// value may be wrapped in single or double quotes, which are stripped. A line
// without '=' is a syntax error, reported with its 1-based line number.
func Parse(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return nil, fmt.Errorf("adkenv: line %d: missing '=' in %q", line, text)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("adkenv: line %d: empty key", line)
		}
		out[key] = unquote(strings.TrimSpace(value))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("adkenv: read: %w", err)
	}
	return out, nil
}

// unquote strips one layer of matching single or double quotes.
func unquote(s string) string {
	if len(s) < 2 {
		return s
	}
	if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// Find walks up from dir looking for apps/.env, returning its path.
//
// It stops at the filesystem root and returns ErrNotFound.
func Find(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("adkenv: abs %q: %w", dir, err)
	}
	for {
		candidate := filepath.Join(abs, "apps", ".env")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("adkenv: stat %q: %w", candidate, err)
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", ErrNotFound
		}
		abs = parent
	}
}

// Load finds apps/.env relative to dir and sets any key that is not already
// present in the process environment. Existing environment variables always
// win, so an explicit `export OPENAI_API_KEY=...` overrides the file.
//
// A key given an empty value is left UNSET rather than exported as empty. The
// template documents blank as "leave this to the provider's own default"
// (apps/.env-example: "Leave blank to use each provider's official API
// endpoint"), and several SDKs distinguish *absent* from *empty* by presence
// alone — openai-go reads OPENAI_BASE_URL with os.LookupEnv and applies
// option.WithBaseURL even when the value is "", which replaces its default with
// an empty base URL and fails the first request with:
//
//	openai: call failed: Post "/responses": unsupported protocol scheme ""
//
// Skipping empty values keeps an unset key unset, so the SDK default survives.
// Callers branching on credentials use Key, which already treats empty as
// absent, so this changes nothing for them.
//
// A missing file is not an error: Load returns ErrNotFound, which callers are
// expected to tolerate and fall back to key-free behaviour.
func Load(dir string) error {
	path, err := Find(dir)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("adkenv: open %q: %w", path, err)
	}
	defer f.Close()

	pairs, err := Parse(f)
	if err != nil {
		return err
	}
	for key, value := range pairs {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, ok := os.LookupEnv(key); !ok {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("adkenv: setenv %s: %w", key, err)
			}
		}
	}
	return nil
}

// Key returns the value of the named environment variable and whether it is
// set to a non-empty value.
//
// Prefer this over os.Getenv at call sites that branch on credential presence:
// an empty-but-set variable is the common CI failure mode, and treating it as
// "present" produces a confusing 401 instead of a clear skip.
func Key(name string) (string, bool) {
	v := strings.TrimSpace(os.Getenv(name))
	return v, v != ""
}
