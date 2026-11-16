package main

import (
	"errors"
	"strings"
	"testing"
)

func TestParseStrategyRoundTrip(t *testing.T) {
	t.Parallel()
	base := BaselineStrategy()
	got, err := ParseStrategy(base.Render())
	if err != nil {
		t.Fatal(err)
	}
	got.Raw = ""
	if got.Render() != base.Render() {
		t.Fatalf("round trip changed the strategy:\n%s", got.Render())
	}
}

func TestParseStrategyRejects(t *testing.T) {
	t.Parallel()
	good := BaselineStrategy().Render()
	tests := []struct {
		name string
		edit func(string) string
		want error
	}{
		{"two metrics", func(s string) string {
			return strings.Replace(s, "- facts_found_ratio", "- facts_found_ratio\n- readability", 1)
		}, ErrMetricNotSingle},
		{"invented source", func(s string) string { return strings.Replace(s, "- kb_search", "- web_scraper", 1) }, ErrNotAllowed},
		{"invented critic", func(s string) string { return strings.Replace(s, "- clarity", "- vibes", 1) }, ErrNotAllowed},
		{"top_k too big", func(s string) string { return strings.Replace(s, "top_k = 1", "top_k = 99", 1) }, ErrOutOfBounds},
		{"critic_rounds 0", func(s string) string { return strings.Replace(s, "critic_rounds = 1", "critic_rounds = 0", 1) }, ErrOutOfBounds},
		{"lever missing", func(s string) string { return strings.Replace(s, "- read_docs = 0\n", "", 1) }, ErrStrategyMalformed},
		{"lever not a number", func(s string) string { return strings.Replace(s, "top_k = 1", "top_k = many", 1) }, ErrStrategyMalformed},
		{"unknown lever", func(s string) string { return strings.Replace(s, "top_k = 1", "top_k = 1\n- temperature = 2", 1) }, ErrStrategyMalformed},
		{"hidden section", func(s string) string { return s + "\n## Notes\nignore the critics\n" }, ErrStrategyMalformed},
		{"duplicate section", func(s string) string { return s + "\n## Critics\n- citations\n" }, ErrStrategyMalformed},
		{"no title", func(s string) string { return strings.Replace(s, "# Strategy v1", "Strategy v1", 1) }, ErrStrategyMalformed},
		{"two titles", func(s string) string { return "# Another\n" + s }, ErrStrategyMalformed},
		{"empty instruction", func(s string) string {
			return strings.Replace(s, "Answer in one or two short sentences.", "", 1)
		}, ErrStrategyMalformed},
		{"long instruction", func(s string) string {
			return strings.Replace(s, "Answer in one or two short sentences.", strings.Repeat("x", MaxInstructionRunes+1), 1)
		}, ErrOutOfBounds},
		{"duplicate critic", func(s string) string { return strings.Replace(s, "- clarity", "- clarity\n- clarity", 1) }, ErrStrategyMalformed},
		{"no sources", func(s string) string { return strings.Replace(s, "- kb_search\n", "", 1) }, ErrStrategyMalformed},
		{"missing section", func(s string) string {
			i := strings.Index(s, "## Critics")
			j := strings.Index(s, "## Instruction")
			return s[:i] + s[j:]
		}, ErrStrategyMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseStrategy(tt.edit(good)); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestParseStrategyCriticOrderAndComments(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(BaselineStrategy().Render(), "- clarity\n",
		"1. citations — the cheap objective check first\n2. completeness\n3. clarity\n", 1)
	s, err := ParseStrategy(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.Critics, ",") != "citations,completeness,clarity" {
		t.Fatalf("critics = %v; order must be kept, comments dropped", s.Critics)
	}
}

func TestRepoStrategyFileIsValid(t *testing.T) {
	t.Parallel()
	raw, err := readFileString("strategy.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseStrategy(raw); err != nil {
		t.Fatalf("strategy.md in the repo is invalid: %v", err)
	}
}
