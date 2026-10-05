// Package chunkeval measures a chunking result: sizes, whole tables, and the
// naive fixed-window baseline that the lab README compares against.
//
// These numbers are the graded evidence of the lab ("Tables: 3/3 preserved",
// min/avg/max size), so they are computed the same way for every student and
// the student's code stays focused on producing the chunks.
package chunkeval

import (
	"fmt"
	"strings"
)

// Tokens approximates the token count of text by its whitespace-separated
// words. It is a stable, offline proxy, not a real tokenizer: BPE models give
// roughly 1.3–2 tokens per word, more for Ukrainian.
func Tokens(text string) int { return len(strings.Fields(text)) }

// Sizes summarises chunk sizes in Tokens.
type Sizes struct {
	N   int     `json:"n"`
	Min int     `json:"min"`
	Avg float64 `json:"avg"`
	Max int     `json:"max"`
}

// Measure returns the size summary of texts. The zero Sizes means no texts.
func Measure(texts []string) Sizes {
	if len(texts) == 0 {
		return Sizes{}
	}
	s := Sizes{N: len(texts), Min: Tokens(texts[0])}
	total := 0
	for _, t := range texts {
		n := Tokens(t)
		total += n
		s.Min = min(s.Min, n)
		s.Max = max(s.Max, n)
	}
	s.Avg = float64(total) / float64(len(texts))
	return s
}

// String renders the summary for a report line.
func (s Sizes) String() string {
	return fmt.Sprintf("n=%d, tokens min/avg/max = %d/%.1f/%d", s.N, s.Min, s.Avg, s.Max)
}

// Preserved counts the tables that appear whole inside at least one chunk.
// Whitespace is normalised, so re-wrapped lines still count; a missing row or
// header does not.
func Preserved(tables, chunks []string) int {
	norm := make([]string, len(chunks))
	for i, c := range chunks {
		norm[i] = squash(c)
	}
	kept := 0
	for _, t := range tables {
		want := squash(t)
		for _, c := range norm {
			if want != "" && strings.Contains(c, want) {
				kept++
				break
			}
		}
	}
	return kept
}

// NaiveWindows is the baseline the lab argues against: flatten the text and
// cut it into windows of size words with overlap words shared between
// neighbours. It ignores structure on purpose — a table that crosses a window
// boundary is split, and the second half loses its header row.
func NaiveWindows(text string, size, overlap int) []string {
	words := strings.Fields(text)
	if size <= 0 || len(words) == 0 {
		return nil
	}
	if overlap < 0 || overlap >= size {
		overlap = 0
	}
	var out []string
	for start := 0; ; start += size - overlap {
		end := min(start+size, len(words))
		out = append(out, strings.Join(words[start:end], " "))
		if end == len(words) {
			return out
		}
	}
}

func squash(s string) string { return strings.Join(strings.Fields(s), " ") }
