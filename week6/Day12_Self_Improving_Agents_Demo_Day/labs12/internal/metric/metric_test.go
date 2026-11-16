package metric

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/ledgerkb"
)

func TestScoreIsPureAndNormalized(t *testing.T) {
	t.Parallel()
	facts := []Fact{
		{Name: "limit", Type: TypeNumber, Forms: []string{"2500 eur"}},
		{Name: "time", Type: TypeDuration, Forms: []string{"5 business days", "five business days"}},
		{Name: "who", Type: TypeName, Forms: []string{"compliance officer"}},
	}
	tests := []struct {
		text string
		want int
	}{
		{"", 0},
		{"Above **2,500 EUR** a Compliance Officer approves; paid in five business days.", 3},
		{"above 2 500 eur", 0},
		{"2,500 EUR only", 1},
		{"COMPLIANCE   OFFICER", 1},
	}
	for _, tt := range tests {
		for range 2 { // same input, same number
			if got, total := Score(tt.text, facts); got != tt.want || total != 3 {
				t.Errorf("Score(%q) = %d/%d, want %d/3", tt.text, got, total, tt.want)
			}
		}
	}
	if m := Missing("compliance officer", facts); strings.Join(m, ",") != "limit,time" {
		t.Errorf("Missing = %v", m)
	}
	if Ratio(1, 4) != 0.25 || Ratio(0, 0) != 0 {
		t.Error("Ratio wrong")
	}
}

func TestByType(t *testing.T) {
	t.Parallel()
	cases := []Case{
		{ID: "A", Facts: []Fact{{Type: TypeNumber, Forms: []string{"5%"}}, {Type: TypeName, Forms: []string{"sre"}}}},
		{ID: "B", Facts: []Fact{{Type: TypeNumber, Forms: []string{"0.8%"}}}},
	}
	got := ByType(cases, []string{"reserve 5% by SRE", "nothing"})
	if got[TypeNumber] != (Tally{1, 2}) || got[TypeName] != (Tally{1, 1}) {
		t.Fatalf("ByType = %+v", got)
	}
	if TotalFacts(cases) != 3 {
		t.Fatal("TotalFacts wrong")
	}
}

func TestLeaks(t *testing.T) {
	t.Parallel()
	cases := []Case{{ID: "A", Facts: []Fact{
		{Forms: []string{"compliance officer"}}, {Forms: []string{"500 eur"}}, {Forms: []string{"march"}},
	}}}
	got := Leaks("Ask the Compliance Officer; the cap is 500 EUR. Audit in March.", cases)
	if strings.Join(got, ";") != "500 eur;compliance officer" {
		t.Fatalf("Leaks = %v (single plain words are not leaks)", got)
	}
	if len(Leaks("read every document and cite figures", cases)) != 0 {
		t.Fatal("a method is not a leak")
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	good := Fact{Name: "x", Type: TypeNumber, Forms: []string{"1"}}
	bad := []Set{
		{Name: "empty"},
		{Name: "noid", Cases: []Case{{Question: "q", Facts: []Fact{good}}}},
		{Name: "dup", Cases: []Case{{ID: "A", Question: "q", Facts: []Fact{good}}, {ID: "A", Question: "q", Facts: []Fact{good}}}},
		{Name: "nofacts", Cases: []Case{{ID: "A", Question: "q"}}},
		{Name: "type", Cases: []Case{{ID: "A", Question: "q", Facts: []Fact{{Name: "x", Type: "vibe", Forms: []string{"1"}}}}}},
		{Name: "forms", Cases: []Case{{ID: "A", Question: "q", Facts: []Fact{{Name: "x", Type: TypeName}}}}},
	}
	for _, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("Validate(%s) = nil, want error", s.Name)
		}
	}
}

// The repo's eval sets must be loadable, big enough, answerable from the
// knowledge base, and disjoint (generalization questions are new questions).
func TestRepoEvalSets(t *testing.T) {
	t.Parallel()
	dev, gen, err := LoadDir(filepath.Join("..", "..", "eval"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dev.Cases) < 5 || len(gen.Cases) < 3 {
		t.Fatalf("dev=%d gen=%d cases; need ≥5 and ≥3", len(dev.Cases), len(gen.Cases))
	}
	var corpus strings.Builder
	for _, d := range ledgerkb.New().Docs() {
		corpus.WriteString(d.Body)
		corpus.WriteString("\n")
	}
	devForms := map[string]bool{}
	for _, c := range dev.Cases {
		for _, f := range c.Facts {
			if !Found(corpus.String(), f) {
				t.Errorf("dev %s/%s: no form found in the knowledge base", c.ID, f.Name)
			}
			devForms[f.Name] = true
		}
	}
	for _, c := range gen.Cases {
		for _, f := range c.Facts {
			if !Found(corpus.String(), f) {
				t.Errorf("gen %s/%s: no form found in the knowledge base", c.ID, f.Name)
			}
			if devForms[f.Name] {
				t.Errorf("gen %s/%s also in dev: generalization must use new facts", c.ID, f.Name)
			}
		}
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, _, err := LoadDir(dir); !errors.Is(err, ErrNoSet) {
		t.Fatalf("LoadDir(empty) = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dev.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(dir, "dev.json")); err == nil {
		t.Fatal("bad json accepted")
	}
	ok := `{"cases":[{"id":"A","question":"q","facts":[{"name":"x","type":"name","forms":["y"]}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "dev.json"), []byte(ok), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generalization.json"), []byte(`{"cases":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadDir(dir); err == nil {
		t.Fatal("empty generalization set accepted")
	}
	s, err := Load(filepath.Join(dir, "dev.json"))
	if err != nil || s.Name != "dev" {
		t.Fatalf("Load = %+v, %v (name defaults to file name)", s, err)
	}
}
