package ledgerkb

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
)

func allText(kb *KB) string {
	var b strings.Builder
	for _, d := range kb.Docs() {
		b.WriteString(d.Body + "\n")
	}
	return b.String()
}

// The eval set is only a metric if every golden fact can actually be found in
// the corpus; a fact no document contains would cap the score for ever.
func TestEveryGoldenFactIsInTheCorpus(t *testing.T) {
	t.Parallel()
	text := allText(New())
	for _, c := range append(Dev(), Heldout()...) {
		if found, total := Score(text, c.Facts); found != total {
			t.Errorf("%s: corpus contains %d of %d facts (missing %v)", c.ID, found, total, Missing(text, c.Facts))
		}
	}
}

// ...and the cheap strategy must really be cheap: the snippets kb_search shows
// must not answer the questions, or "search once" would already be optimal and
// the loop would have nothing to improve.
func TestSnippetsAloneDoNotSolveTheDevSet(t *testing.T) {
	t.Parallel()
	kb := New()
	found, total := 0, 0
	for _, c := range Dev() {
		var sb strings.Builder
		for _, h := range kb.Search(c.Question, 3) {
			sb.WriteString(h.Snippet + " ")
		}
		f, n := Score(sb.String(), c.Facts)
		found, total = found+f, total+n
	}
	if got := float64(found) / float64(total); got > 0.25 {
		t.Errorf("snippets alone score %.2f (%d/%d); the baseline would be too strong", got, found, total)
	}
}

// With the full text of the documents the search ranks first, a correct
// strategy can reach a high score: the ceiling is reachable.
func TestTopThreeDocsCoverMostDevFacts(t *testing.T) {
	t.Parallel()
	kb := New()
	found, total := 0, 0
	for _, c := range Dev() {
		var sb strings.Builder
		for _, h := range kb.Search(c.Question, 3) {
			d, _ := kb.Get(h.ID)
			sb.WriteString(d.Body + " ")
		}
		f, n := Score(sb.String(), c.Facts)
		found, total = found+f, total+n
	}
	if got := float64(found) / float64(total); got < 0.85 {
		t.Errorf("reading the top 3 documents scores only %.2f (%d/%d)", got, found, total)
	}
}

func TestSearchIsDeterministicAndRanked(t *testing.T) {
	t.Parallel()
	kb := New()
	a := kb.Search("chargeback evidence deadline", 3)
	b := kb.Search("chargeback evidence deadline", 3)
	if len(a) == 0 || a[0].ID != "CHB-002" {
		t.Fatalf("top hit = %+v, want CHB-002", a)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("search not deterministic at %d", i)
		}
	}
	if got := kb.Search("zzzz qqqq", 3); len(got) != 0 {
		t.Errorf("nonsense query returned %v", got)
	}
	if got := kb.Search("refund", 1); len(got) != 1 {
		t.Errorf("k=1 returned %d hits", len(got))
	}
	short := NewFrom([]Doc{{ID: "X-1", Title: "t", Body: "refund"}}).Search("refund", 3)
	if short[0].Snippet != "refund" {
		t.Errorf("short body must not be truncated: %q", short[0].Snippet)
	}
}

func TestGetIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	if _, ok := New().Get(" ref-001 "); !ok {
		t.Error("Get(ref-001) failed")
	}
	if _, ok := New().Get("NOPE"); ok {
		t.Error("Get(NOPE) succeeded")
	}
}

func TestScoreMissingAndNormalize(t *testing.T) {
	t.Parallel()
	facts := []Fact{f("a", "5 business days", "five business days"), f("b", "15 eur")}
	if got, n := Score("Within five business days.", facts); got != 1 || n != 2 {
		t.Errorf("Score = %d/%d, want 1/2", got, n)
	}
	if got := Missing("Within five business days.", facts); len(got) != 1 || got[0] != "b" {
		t.Errorf("Missing = %v", got)
	}
	if Normalize("A–B  C") != "a-b c" {
		t.Errorf("Normalize = %q", Normalize("A–B  C"))
	}
	if TotalFacts(Dev()) != 24 {
		t.Errorf("TotalFacts(Dev) = %d, want 24", TotalFacts(Dev()))
	}
}

func TestLeaks(t *testing.T) {
	t.Parallel()
	dev := Dev()
	if got := Leaks("Always quote figures exactly and ask Compliance when unsure about a token.", dev); len(got) != 0 {
		t.Errorf("vocabulary flagged as leak: %v", got)
	}
	got := Leaks("Remember: chargeback fee is 15 EUR and send it to the Fraud team.", dev)
	if len(got) != 2 {
		t.Errorf("Leaks = %v, want the figure and the phrase", got)
	}
	if got := Leaks("No facts here at all", dev); len(got) != 0 {
		t.Errorf("Leaks = %v", got)
	}
	if got := Leaks("15 eur and 15 EUR", dev); len(got) != 1 {
		t.Errorf("duplicates must collapse: %v", got)
	}
}

func TestToolsThroughAnAgent(t *testing.T) {
	t.Parallel()
	kb := New()
	if _, err := kb.Tool("web_fetch"); err == nil {
		t.Error("unknown tool accepted")
	}
	tools, err := kb.Tools(AllToolNames())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kb.Tools([]string{"nope"}); err == nil {
		t.Error("Tools accepted unknown name")
	}
	m := fakellm.New("fake",
		fakellm.CallTurn(ToolSearch, map[string]any{"query": "chargeback evidence"}),
		fakellm.CallTurn(ToolRead, map[string]any{"id": "CHB-002"}),
		fakellm.CallTurn(ToolRead, map[string]any{"id": "NOPE-1"}),
		fakellm.CallTurn(ToolSearch, map[string]any{"query": ""}),
		fakellm.CallTurn(ToolSearch, map[string]any{"query": "zzzz"}),
		fakellm.TextTurn("done"),
	)
	a, err := llmagent.New(llmagent.Config{Name: "w", Model: m, Instruction: "x", Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	var _ agent.Agent = a
	res, err := labrun.Run(context.Background(), a, "q")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 5 || res.Final != "done" {
		t.Fatalf("calls=%v final=%q", res.ToolCalls, res.Final)
	}
	body := ""
	for _, r := range res.ToolResults {
		body += strings.ToLower(strings.TrimSpace(strings.Join(keys(r.Response), ","))) + ";"
	}
	if !strings.Contains(body, "hits") || !strings.Contains(body, "body") {
		t.Errorf("tool responses lack hits/body keys: %s", body)
	}
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
