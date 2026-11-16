package main

// Brake (d): the strategy is a separate, human-readable artifact (strategy.md,
// the analogue of program.md in karpathy/autoresearch). People edit it in a
// PR, the agent proposes changes to it, and both read the same file.
//
// The file is parsed and validated, not accepted as free text. The parser is
// where three more brakes live: exactly one metric line, allowlists for
// sources and critics, and hard bounds on every numeric lever.

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Allowlists. A source or critic the strategy invents is refused: the
// pipeline is built literally from these lists.
var (
	allowedSources = []string{SourceKB, SourceGraph}
	allowedCritics = []string{CriticCitations, CriticCompleteness, CriticClarity}
)

// Source and critic names.
const (
	SourceKB    = "kb_search"
	SourceGraph = "graph_lookup"

	CriticCitations    = "citations"
	CriticCompleteness = "completeness"
	CriticClarity      = "clarity"
)

// MaxInstructionRunes caps the editable instruction: without it a "better"
// strategy could win by pasting half of the knowledge base into it.
const MaxInstructionRunes = 1200

// Bounds for the numeric levers: name → [min, max].
var leverBounds = map[string][2]int{
	"max_subqueries": {1, 6},
	"top_k":          {1, 8},
	"read_docs":      {0, 4},
	"critic_rounds":  {1, 3},
	"max_attempts":   {1, 50},
}

// Strategy validation errors, kept apart so the audit log names the incident.
var (
	ErrStrategyMalformed = errors.New("стратегія має некоректний формат")
	ErrMetricNotSingle   = errors.New("стратегія мусить оголошувати рівно одну метрику")
	ErrNotAllowed        = errors.New("поза allowlist")
	ErrOutOfBounds       = errors.New("значення поза межами")
)

// Strategy is a parsed strategy.md.
type Strategy struct {
	Title         string
	Metric        string
	Sources       []string
	MaxSubqueries int
	TopK          int
	ReadDocs      int
	Critics       []string // order matters: they run in this order
	Instruction   string
	MaxAttempts   int
	CriticRounds  int
	Raw           string
}

const (
	secMetric  = "metric"
	secSources = "sources"
	secSearch  = "search"
	secCritics = "critics"
	secInstr   = "instruction"
	secStop    = "stop conditions"
)

// ParseStrategy parses and validates a strategy document.
func ParseStrategy(raw string) (Strategy, error) {
	s := Strategy{Raw: raw}
	sections, title, err := splitSections(raw)
	if err != nil {
		return Strategy{}, err
	}
	s.Title = title

	metrics := sections[secMetric]
	if len(metrics) != 1 || stripBullet(metrics[0]) == "" {
		return Strategy{}, fmt.Errorf("%w: знайдено %d рядків у `## Metric`", ErrMetricNotSingle, len(metrics))
	}
	s.Metric = stripBullet(metrics[0])

	if s.Sources, err = namedList(sections, secSources, allowedSources); err != nil {
		return Strategy{}, err
	}
	if s.Critics, err = namedList(sections, secCritics, allowedCritics); err != nil {
		return Strategy{}, err
	}

	levers, err := parseLevers(append(sections[secSearch], sections[secStop]...))
	if err != nil {
		return Strategy{}, err
	}
	s.MaxSubqueries, s.TopK, s.ReadDocs = levers["max_subqueries"], levers["top_k"], levers["read_docs"]
	s.MaxAttempts, s.CriticRounds = levers["max_attempts"], levers["critic_rounds"]

	s.Instruction = strings.TrimSpace(strings.Join(sections[secInstr], "\n"))
	if s.Instruction == "" {
		return Strategy{}, fmt.Errorf("%w: секція `## Instruction` порожня або відсутня", ErrStrategyMalformed)
	}
	if n := len([]rune(s.Instruction)); n > MaxInstructionRunes {
		return Strategy{}, fmt.Errorf("%w: інструкція має %d символів, ліміт %d", ErrOutOfBounds, n, MaxInstructionRunes)
	}
	return s, nil
}

// splitSections reads `# title` and `## section` blocks. Unknown or repeated
// sections are errors: text a reviewer sees but the agent never reads is the
// perfect place to hide something.
func splitSections(raw string) (map[string][]string, string, error) {
	sections := map[string][]string{}
	var title, current string
	for line := range strings.SplitSeq(raw, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
		case strings.HasPrefix(t, "## "):
			current = strings.ToLower(strings.TrimSpace(t[3:]))
			switch current {
			case secMetric, secSources, secSearch, secCritics, secInstr, secStop:
			default:
				return nil, "", fmt.Errorf("%w: невідома секція %q", ErrStrategyMalformed, current)
			}
			if _, seen := sections[current]; seen {
				return nil, "", fmt.Errorf("%w: секція %q оголошена двічі", ErrStrategyMalformed, current)
			}
			sections[current] = nil
		case strings.HasPrefix(t, "# "):
			if title != "" {
				return nil, "", fmt.Errorf("%w: більше одного заголовка `# `", ErrStrategyMalformed)
			}
			title = strings.TrimSpace(t[2:])
		case current != "":
			sections[current] = append(sections[current], t)
		}
	}
	if title == "" {
		return nil, "", fmt.Errorf("%w: немає заголовка `# ...`", ErrStrategyMalformed)
	}
	for _, need := range []string{secMetric, secSources, secSearch, secCritics, secInstr, secStop} {
		if _, ok := sections[need]; !ok {
			return nil, "", fmt.Errorf("%w: немає секції `## %s`", ErrStrategyMalformed, need)
		}
	}
	return sections, title, nil
}

// namedList reads a bullet list of names checked against an allowlist.
func namedList(sections map[string][]string, sec string, allow []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, line := range sections[sec] {
		name := stripBullet(line)
		if name == "" {
			continue
		}
		if !slices.Contains(allow, name) {
			return nil, fmt.Errorf("%w: %q у `## %s` (дозволені: %s)", ErrNotAllowed, name, sec, strings.Join(allow, ", "))
		}
		if seen[name] {
			return nil, fmt.Errorf("%w: %q повторюється в `## %s`", ErrStrategyMalformed, name, sec)
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: `## %s` порожня", ErrStrategyMalformed, sec)
	}
	return out, nil
}

// parseLevers reads `name = N` lines; every known lever is required once and
// must be within its bounds.
func parseLevers(lines []string) (map[string]int, error) {
	out := map[string]int{}
	for _, line := range lines {
		k, v, ok := strings.Cut(stripBulletKeepSpaces(line), "=")
		if !ok {
			return nil, fmt.Errorf("%w: рядок %q не має форми `name = N`", ErrStrategyMalformed, line)
		}
		name := strings.TrimSpace(strings.ToLower(k))
		b, known := leverBounds[name]
		if !known {
			return nil, fmt.Errorf("%w: невідомий параметр %q", ErrStrategyMalformed, name)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("%w: параметр %q повторюється", ErrStrategyMalformed, name)
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("%w: %s = %q не є числом", ErrStrategyMalformed, name, strings.TrimSpace(v))
		}
		if n < b[0] || n > b[1] {
			return nil, fmt.Errorf("%w: %s = %d, дозволено %d..%d", ErrOutOfBounds, name, n, b[0], b[1])
		}
		out[name] = n
	}
	for name := range leverBounds {
		if _, ok := out[name]; !ok {
			return nil, fmt.Errorf("%w: немає параметра %s", ErrStrategyMalformed, name)
		}
	}
	return out, nil
}

func stripBullet(line string) string {
	s := stripBulletKeepSpaces(line)
	if i := strings.IndexAny(s, " \t—"); i > 0 {
		s = s[:i] // "citations — check figures" → "citations"
	}
	return strings.TrimSpace(s)
}

func stripBulletKeepSpaces(line string) string {
	s := strings.TrimSpace(line)
	for _, p := range []string{"- ", "* "} {
		s = strings.TrimPrefix(s, p)
	}
	if i := strings.Index(s, ". "); i > 0 && i <= 3 {
		if _, err := strconv.Atoi(s[:i]); err == nil {
			s = s[i+2:]
		}
	}
	return strings.TrimSpace(s)
}

// Render builds the canonical text of a strategy.
func (s Strategy) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n## Metric\n- %s\n\n## Sources\n", s.Title, s.Metric)
	for _, x := range s.Sources {
		fmt.Fprintf(&b, "- %s\n", x)
	}
	fmt.Fprintf(&b, "\n## Search\n- max_subqueries = %d\n- top_k = %d\n- read_docs = %d\n\n## Critics\n",
		s.MaxSubqueries, s.TopK, s.ReadDocs)
	for _, x := range s.Critics {
		fmt.Fprintf(&b, "- %s\n", x)
	}
	fmt.Fprintf(&b, "\n## Instruction\n%s\n\n## Stop conditions\n- max_attempts = %d\n- critic_rounds = %d\n",
		s.Instruction, s.MaxAttempts, s.CriticRounds)
	return b.String()
}

// BaselineStrategy is the deliberately weak starting point: one query, one
// snippet, no full documents, one cosmetic critic.
func BaselineStrategy() Strategy {
	return Strategy{
		Title:         "Strategy v1: one search, answer from the best snippet",
		Metric:        "facts_found_ratio",
		Sources:       []string{SourceKB},
		MaxSubqueries: 1,
		TopK:          1,
		ReadDocs:      0,
		Critics:       []string{CriticClarity},
		Instruction:   "Answer in one or two short sentences.",
		MaxAttempts:   20,
		CriticRounds:  1,
	}
}
