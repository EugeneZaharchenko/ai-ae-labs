// Package metric is brake (c): the one objective number the loop optimizes.
//
// score = facts_found / facts_total over a labelled eval set. Score is a pure
// function of the report text: no model, no I/O, the same input always gives
// the same number. That is the verifiability constraint — a property you can't
// turn into a comparable float64 can't drive keep/discard.
//
// Facts are typed (number, duration, date, name, quote) so the eval report can
// say WHICH kinds of facts a strategy finds better, not only how many.
package metric

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Fact types.
const (
	TypeNumber   = "number"
	TypeDuration = "duration"
	TypeDate     = "date"
	TypeName     = "name"
	TypeQuote    = "quote"
)

var knownTypes = map[string]bool{TypeNumber: true, TypeDuration: true, TypeDate: true, TypeName: true, TypeQuote: true}

// Fact is one thing a correct report must contain. Any of Forms counts: they
// are spellings of the same fact, not alternative facts.
type Fact struct {
	Name  string   `json:"name"`
	Type  string   `json:"type"`
	Forms []string `json:"forms"`
}

// Case is one eval question with its expected facts.
type Case struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Facts    []Fact `json:"facts"`
}

// Set is a named eval set, as loaded from one JSON file.
type Set struct {
	Name    string `json:"name"`
	Created string `json:"created"`
	Cases   []Case `json:"cases"`
}

// Normalize lowercases, unifies typographic variants and drops thousands
// separators, so "2,500 EUR" and "2500 eur" match.
func Normalize(s string) string {
	r := strings.NewReplacer(" ", " ", " ", " ", "–", "-", "—", "-", "‑", "-",
		"**", "", "`", "", "“", "\"", "”", "\"", "’", "'")
	s = strings.ToLower(r.Replace(s))
	var b strings.Builder
	rs := []rune(s)
	for i, c := range rs {
		if c == ',' && i > 0 && i+1 < len(rs) && unicode.IsDigit(rs[i-1]) && unicode.IsDigit(rs[i+1]) {
			continue
		}
		b.WriteRune(c)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// Found reports whether fact occurs in text.
func Found(text string, fact Fact) bool {
	t := Normalize(text)
	for _, form := range fact.Forms {
		if f := Normalize(form); f != "" && strings.Contains(t, f) {
			return true
		}
	}
	return false
}

// Score counts the facts present in text.
func Score(text string, facts []Fact) (found, total int) {
	for _, f := range facts {
		if Found(text, f) {
			found++
		}
	}
	return found, len(facts)
}

// Missing names the facts absent from text (audit detail only).
func Missing(text string, facts []Fact) []string {
	var out []string
	for _, f := range facts {
		if !Found(text, f) {
			out = append(out, f.Name)
		}
	}
	return out
}

// Ratio is found/total with 0 for an empty set.
func Ratio(found, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(found) / float64(total)
}

// Tally is found/total for one fact type.
type Tally struct {
	Found, Total int
}

// ByType breaks the result down per fact type: answers[i] is the report for cases[i].
func ByType(cases []Case, answers []string) map[string]Tally {
	out := map[string]Tally{}
	for i, c := range cases {
		text := ""
		if i < len(answers) {
			text = answers[i]
		}
		for _, f := range c.Facts {
			t := out[f.Type]
			t.Total++
			if Found(text, f) {
				t.Found++
			}
			out[f.Type] = t
		}
	}
	return out
}

// TotalFacts sums the facts over cases.
func TotalFacts(cases []Case) int {
	n := 0
	for _, c := range cases {
		n += len(c.Facts)
	}
	return n
}

// Leaks returns expected forms that appear verbatim in text. A strategy that
// contains them is a cheat sheet, not a method.
func Leaks(text string, cases []Case) []string {
	t := Normalize(text)
	seen := map[string]bool{}
	var out []string
	for _, c := range cases {
		for _, f := range c.Facts {
			for _, form := range f.Forms {
				nf := Normalize(form)
				if leakable(nf) && strings.Contains(t, nf) && !seen[nf] {
					seen[nf] = true
					out = append(out, form)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// leakable: a figure or a multi-word phrase. Single plain words ("compliance")
// are vocabulary a legitimate method may use.
func leakable(form string) bool {
	if strings.ContainsRune(form, ' ') {
		return true
	}
	return len(form) >= 3 && strings.IndexFunc(form, unicode.IsDigit) >= 0
}

// Validate checks a set: ≥1 case, unique ids, each case with facts of a known type.
func (s Set) Validate() error {
	if len(s.Cases) == 0 {
		return fmt.Errorf("eval set %q: немає питань", s.Name)
	}
	seen := map[string]bool{}
	for _, c := range s.Cases {
		switch {
		case c.ID == "" || c.Question == "":
			return fmt.Errorf("eval set %q: питання без id або тексту", s.Name)
		case seen[c.ID]:
			return fmt.Errorf("eval set %q: id %s повторюється", s.Name, c.ID)
		case len(c.Facts) == 0:
			return fmt.Errorf("eval set %q: %s без очікуваних фактів", s.Name, c.ID)
		}
		seen[c.ID] = true
		for _, f := range c.Facts {
			if !knownTypes[f.Type] {
				return fmt.Errorf("eval set %q: %s/%s має невідомий тип %q", s.Name, c.ID, f.Name, f.Type)
			}
			if len(f.Forms) == 0 {
				return fmt.Errorf("eval set %q: %s/%s без форм", s.Name, c.ID, f.Name)
			}
		}
	}
	return nil
}

// Load reads one eval set file.
func Load(path string) (Set, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Set{}, fmt.Errorf("eval: %w", err)
	}
	var s Set
	if err := json.Unmarshal(data, &s); err != nil {
		return Set{}, fmt.Errorf("eval %s: %w", filepath.Base(path), err)
	}
	if s.Name == "" {
		s.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return s, s.Validate()
}

// ErrNoSet reports a missing eval set in a directory.
var ErrNoSet = errors.New("eval: набір не знайдено")

// LoadDir reads dev.json and generalization.json from dir. The generalization
// set is optional; dev is not.
func LoadDir(dir string) (dev, gen Set, err error) {
	dev, err = Load(filepath.Join(dir, "dev.json"))
	if err != nil {
		return Set{}, Set{}, fmt.Errorf("%w: %w", ErrNoSet, err)
	}
	genPath := filepath.Join(dir, "generalization.json")
	if _, statErr := os.Stat(genPath); statErr == nil {
		gen, err = Load(genPath)
		if err != nil {
			return Set{}, Set{}, err
		}
	}
	return dev, gen, nil
}
