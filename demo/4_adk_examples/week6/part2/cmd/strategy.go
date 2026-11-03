package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The strategy: the ONE artifact the agent is allowed to change.
//
// It is a human-readable, human-reviewable file (strategy.md), separate from the
// code that runs the loop — the autoresearch idea that the "research org"
// instructions are an editable artifact of their own. A human can read the
// before and after, diff them, and revert one with `git checkout`. What the
// strategy controls is narrow and real: the worker's method (its instruction)
// and which tools it is given. It cannot touch the metric, the budget, the eval
// set or the loop, all of which live in code.

// allowedTools is the allowlist a strategy may draw from. A tool the strategy
// names that is not here is rejected before anything runs.
var allowedTools = []string{ToolSearch, ToolRead}

// maxInstructionRunes bounds the strategy's size. An unbounded instruction is
// an unbounded token bill on every question of every experiment, and a place to
// hide a prompt-injection payload.
const maxInstructionRunes = 600

// ErrInvalidStrategy marks a proposal that does not satisfy the schema.
var ErrInvalidStrategy = errors.New("invalid strategy")

// Strategy is the parsed form of strategy.md.
type Strategy struct {
	Title       string
	Tools       []string
	Instruction string
}

// Baseline is where the loop starts: search only, so the worker can see which
// document is relevant but never read it.
func Baseline() Strategy {
	return Strategy{
		Title:       "Baseline",
		Tools:       []string{ToolSearch},
		Instruction: "Search the knowledge base and answer from the results.",
	}
}

// Render writes the strategy in its file form.
func (s Strategy) Render() string {
	return fmt.Sprintf("# %s\ntools: %s\n\n%s\n", s.Title, strings.Join(s.Tools, ", "), strings.TrimSpace(s.Instruction))
}

// Validate enforces the schema. Every rule exists because of a failure mode.
func (s Strategy) Validate() error {
	switch {
	case strings.TrimSpace(s.Instruction) == "":
		return fmt.Errorf("%w: empty instruction", ErrInvalidStrategy)
	case len([]rune(s.Instruction)) > maxInstructionRunes:
		return fmt.Errorf("%w: instruction is %d characters, the limit is %d", ErrInvalidStrategy, len([]rune(s.Instruction)), maxInstructionRunes)
	case len(s.Tools) == 0:
		return fmt.Errorf("%w: no tools", ErrInvalidStrategy)
	}
	for _, t := range s.Tools {
		if !slices.Contains(allowedTools, t) {
			return fmt.Errorf("%w: %q is not an allowed tool (allowed: %s)", ErrInvalidStrategy, t, strings.Join(allowedTools, ", "))
		}
	}
	return nil
}

// ParseStrategy reads the file form.
func ParseStrategy(text string) (Strategy, error) {
	var s Strategy
	var body []string
	seenTools := false
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		switch {
		case s.Title == "" && strings.HasPrefix(line, "# "):
			s.Title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
		case !seenTools && strings.HasPrefix(line, "tools:"):
			seenTools = true
			for _, t := range strings.Split(strings.TrimPrefix(line, "tools:"), ",") {
				if t = strings.TrimSpace(t); t != "" {
					s.Tools = append(s.Tools, t)
				}
			}
		default:
			body = append(body, line)
		}
	}
	s.Instruction = strings.TrimSpace(strings.Join(body, "\n"))
	if s.Title == "" || !seenTools {
		return Strategy{}, fmt.Errorf("%w: needs a '# title' line and a 'tools:' line", ErrInvalidStrategy)
	}
	return s, nil
}

// DefaultPath is the strategy file's name inside the working directory.
const DefaultPath = "strategy.md"

// ParseProposal reads a proposer's raw answer: an optional `path:` line naming
// where the strategy should be written, then the strategy itself, possibly in a
// code fence. The path is the PROPOSER'S claim and is untrusted: the loop checks
// it against the declared surface before anything is written.
func ParseProposal(raw string) (path string, s Strategy, err error) {
	body := strings.TrimSpace(unfence(raw))
	path = DefaultPath
	if first, rest, ok := strings.Cut(body, "\n"); ok && strings.HasPrefix(strings.TrimSpace(first), "path:") {
		path = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(first), "path:"))
		body = rest
	}
	s, err = ParseStrategy(body)
	return path, s, err
}

// unfence strips a Markdown code fence, if there is one.
func unfence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			return rest[:j]
		}
		return rest
	}
	return s
}
