package chunkeval

import (
	"reflect"
	"strings"
	"testing"
)

func TestTokens(t *testing.T) {
	t.Parallel()
	tests := map[string]int{"": 0, "  ": 0, "one": 1, "a b\nc\td": 4, "| A | B |": 5}
	for in, want := range tests {
		if got := Tokens(in); got != want {
			t.Errorf("Tokens(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestMeasure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		texts []string
		want  Sizes
	}{
		{"none", nil, Sizes{}},
		{"one", []string{"a b c"}, Sizes{N: 1, Min: 3, Avg: 3, Max: 3}},
		{"three", []string{"a", "a b c d e", "a b c"}, Sizes{N: 3, Min: 1, Avg: 3, Max: 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Measure(tt.texts); got != tt.want {
				t.Errorf("Measure() = %+v, want %+v", got, tt.want)
			}
		})
	}
	if got := (Sizes{N: 3, Min: 1, Avg: 3, Max: 5}).String(); got != "n=3, tokens min/avg/max = 1/3.0/5" {
		t.Errorf("String() = %q", got)
	}
}

func TestPreserved(t *testing.T) {
	t.Parallel()
	tbl := "| A | B |\n|---|---|\n| 1 | 2 |"
	tests := []struct {
		name   string
		chunks []string
		want   int
	}{
		{"whole", []string{"intro", "text before\n" + tbl + "\nafter"}, 1},
		{"rewrapped whitespace", []string{"| A |   B |\n|---|---|\n|  1 | 2 |"}, 1},
		{"split in two", []string{"| A | B |\n|---|---|", "| 1 | 2 |"}, 0},
		{"no chunks", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Preserved([]string{tbl}, tt.chunks); got != tt.want {
				t.Errorf("Preserved() = %d, want %d", got, tt.want)
			}
		})
	}
	if got := Preserved([]string{"  "}, []string{"anything"}); got != 0 {
		t.Errorf("blank table counted as preserved")
	}
}

func TestNaiveWindows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		text          string
		size, overlap int
		want          []string
	}{
		{"exact", "a b c d", 2, 0, []string{"a b", "c d"}},
		{"overlap", "a b c d e", 3, 1, []string{"a b c", "c d e"}},
		{"tail", "a b c", 2, 0, []string{"a b", "c"}},
		{"bad overlap ignored", "a b c", 2, 5, []string{"a b", "c"}},
		{"zero size", "a b", 0, 0, nil},
		{"empty text", "  ", 3, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := NaiveWindows(tt.text, tt.size, tt.overlap); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NaiveWindows() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The point of the baseline: a window boundary inside a table breaks it.
func TestNaiveWindowsSplitTable(t *testing.T) {
	t.Parallel()
	tbl := "| Vendor | Findings |\n|---|---|\n| Acme | 3 |\n| Globex | 1 |"
	doc := strings.Repeat("filler ", 10) + "\n" + tbl
	windows := NaiveWindows(doc, 14, 0)
	if got := Preserved([]string{tbl}, windows); got != 0 {
		t.Errorf("naive windows kept the table whole; pick a size that splits it")
	}
}
