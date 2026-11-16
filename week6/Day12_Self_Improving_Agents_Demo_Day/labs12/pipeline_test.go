package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/ledgerkb"
)

var testLib = NewLibrary(ledgerkb.New())

func strategyWith(edit func(*Strategy)) Strategy {
	s := BaselineStrategy()
	if edit != nil {
		edit(&s)
	}
	s.Raw = s.Render()
	return s
}

func newTestPipeline(t *testing.T, s Strategy, writer, critic model.LLM, gate *Gate) (*Meter, func(q string) labrun.Result) {
	t.Helper()
	meter := NewMeter()
	if gate == nil {
		gate = &Gate{Mode: GateAuto, Ledger: NewLedger(nil)}
	}
	pipe, err := NewPipeline(PipelineConfig{Strategy: s, Lib: testLib, Writer: writer, Critic: critic, Gate: gate, Meter: meter})
	if err != nil {
		t.Fatal(err)
	}
	return meter, func(q string) labrun.Result {
		res, err := labrun.Run(context.Background(), pipe, q)
		if err != nil {
			t.Fatalf("pipeline: %v", err)
		}
		return res
	}
}

func traceOf(res labrun.Result) map[string]string {
	out := map[string]string{}
	for _, e := range res.Events {
		for k, v := range e.Actions.StateDelta {
			if s, ok := v.(string); ok && strings.HasPrefix(k, "w6p2:") {
				out[strings.TrimPrefix(k, "w6p2:")] = s
			}
		}
	}
	return out
}

func TestDecompose(t *testing.T) {
	t.Parallel()
	q := "A merchant settles in UAH. What FX margin applies, at what time are rates fixed, and which team reports it?"
	if got := decompose(q, 1); len(got) != 1 || got[0] != q {
		t.Fatalf("max=1 must keep the question whole: %v", got)
	}
	got := decompose(q, 4)
	if len(got) != 4 || got[0] != q {
		t.Fatalf("decompose = %q", got)
	}
	seen := map[string]bool{}
	for _, p := range got {
		if seen[p] {
			t.Fatalf("duplicate sub-query %q", p)
		}
		seen[p] = true
	}
}

func TestLibrary(t *testing.T) {
	t.Parallel()
	hits := testLib.Search("refund second approval compliance officer", 2)
	if len(hits) == 0 || hits[0].DocID != "REF-001" || !strings.Contains(hits[0].Text, "2,500") {
		t.Fatalf("Search = %+v", hits)
	}
	ps, ents := testLib.GraphLookup("Which severity is SEV1 and who is the Head of Payments?", 2)
	ids := map[string]bool{}
	for _, p := range ps {
		ids[p.DocID] = true
	}
	if !ids["INC-007"] || !ids["SLA-011"] || len(ents) < 2 {
		t.Fatalf("GraphLookup docs=%v entities=%v", ids, ents)
	}
	if full, ok := testLib.Read("PAY-003"); !ok || !strings.Contains(full.Text, "14:00") {
		t.Fatal("Read failed")
	}
	if _, ok := testLib.Read("NOPE-1"); ok || testLib.Has("NOPE-1") {
		t.Fatal("unknown doc found")
	}
	if !containsWord("the sev1 rule", "sev1") || containsWord("sev12", "sev1") {
		t.Fatal("containsWord wrong")
	}
}

func TestCheckCitations(t *testing.T) {
	t.Parallel()
	ev := Evidence{Passages: []Passage{{DocID: "REF-001", Text: "A refund above 2,500 EUR needs a second approval from a Compliance Officer."}}}
	tests := []struct {
		name, report, question string
		pass                   bool
		want                   string
	}{
		{"good", "Above 2,500 EUR a Compliance Officer approves [REF-001].", "", true, ""},
		{"list markers are not figures", "1. Above 2500 EUR a Compliance Officer approves [REF-001].", "", true, ""},
		{"figure from the question", "For 3,000 EUR, above 2,500 EUR a second approval applies [REF-001].", "refund 3,000 EUR?", true, ""},
		{"empty", "  ", "", false, "порожній"},
		{"uncited figure", "Above 2,500 EUR a Compliance Officer approves.", "", false, "без джерела"},
		{"invented id", "Above 2,500 EUR [ZZZ-999].", "", false, "не існує"},
		{"id not in evidence", "Above 2,500 EUR [PAY-003].", "", false, "немає серед"},
		{"invented figure", "Above 9,999 EUR a Compliance Officer approves [REF-001].", "", false, "9999"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := Draft{Question: tt.question, Report: tt.report, Evidence: ev}
			v := checkCitations(d, testLib)
			if v.Passed != tt.pass || !strings.Contains(strings.Join(v.Comments, " "), tt.want) {
				t.Fatalf("verdict = %+v", v)
			}
		})
	}
}

func TestParseVerdict(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, raw, want string }{
		{"ok", `{"critic":"clarity","passed":true}`, ""},
		{"fenced with prose", "Verdict:\n```json\n{\"critic\":\"clarity\",\"passed\":false,\"comments\":[\"split the list\"]}\n```", ""},
		{"prose", "Looks fine!", "JSON"},
		{"broken", `{"critic":"clarity","passed":tru`, "JSON"},
		{"wrong type", `{"critic":"clarity","passed":"yes"}`, "схему"},
		{"missing field", `{"critic":"clarity"}`, "обов'язкового"},
		{"unknown role", `{"critic":"boss","passed":true}`, "невідома"},
		{"role swap", `{"critic":"completeness","passed":true}`, "чекали"},
		{"fail without comment", `{"critic":"clarity","passed":false,"comments":[" "]}`, "без коментаря"},
	}
	for _, tt := range tests {
		v, err := ParseVerdict(tt.raw, CriticClarity)
		if tt.want == "" {
			if err != nil || v.Critic != CriticClarity {
				t.Errorf("%s: %v %+v", tt.name, err, v)
			}
			continue
		}
		if !errors.Is(err, ErrBadVerdict) || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
	}
	if v := rejectedVerdict(CriticClarity, ErrBadVerdict); v.Passed || len(v.Comments) != 1 {
		t.Fatal("a rejected verdict must count as a failure with a comment")
	}
}

func TestPipelineOfflineFullGraph(t *testing.T) {
	t.Parallel()
	s := strategyWith(func(s *Strategy) {
		s.Sources = []string{SourceKB, SourceGraph}
		s.MaxSubqueries, s.TopK = 4, 3
		s.Critics = []string{CriticCitations, CriticCompleteness, CriticClarity}
	})
	_, run := newTestPipeline(t, s, offlineWriter{}, offlineCritic{}, nil)
	res := run("Payouts are down. Which severity is SEV1, how fast is on-call paged, and who is the Head of Payments?")
	tr := traceOf(res)
	for _, node := range []string{"plan", SourceKB, SourceGraph, "collect", "draft_report", "critic_loop#1", "approval_gate", "final_report"} {
		if tr[node] == "" {
			t.Errorf("event log has no line for %s", node)
		}
	}
	if !strings.Contains(tr["critic_loop#1"], "citations: passed; completeness: passed; clarity: passed") {
		t.Errorf("critics = %q", tr["critic_loop#1"])
	}
	if !strings.Contains(res.Final, "затверджено: system:eval") || !strings.Contains(res.Final, "5 minutes") {
		t.Errorf("final report:\n%s", res.Final)
	}
}

// A different strategy must change behaviour: sources, evidence, report.
func TestStrategyChangesBehaviour(t *testing.T) {
	t.Parallel()
	q := "Payouts are down. Which severity is SEV1 and who is the Head of Payments?"
	_, base := newTestPipeline(t, strategyWith(nil), offlineWriter{}, offlineCritic{}, nil)
	_, rich := newTestPipeline(t, strategyWith(func(s *Strategy) {
		s.Sources = []string{SourceKB, SourceGraph}
		s.TopK, s.ReadDocs = 3, 2
	}), offlineWriter{}, offlineCritic{}, nil)
	b, r := base(q), rich(q)
	tb, tr := traceOf(b), traceOf(r)
	if !strings.Contains(tb[SourceGraph], "вимкнено") || strings.Contains(tr[SourceGraph], "вимкнено") {
		t.Fatalf("graph branch: base=%q rich=%q", tb[SourceGraph], tr[SourceGraph])
	}
	if b.Final == r.Final || len(citedIDs(r.Final)) <= len(citedIDs(b.Final)) {
		t.Fatalf("the richer strategy must cite more sources:\nbase %v\nrich %v", citedIDs(b.Final), citedIDs(r.Final))
	}
}

func TestCriticLoopRevisesAnInventedFigure(t *testing.T) {
	t.Parallel()
	writer := fakellm.New("writer",
		fakellm.TextTurn("A refund above 9,999 EUR needs a Compliance Officer [REF-001]."),
		fakellm.TextTurn("A refund above 2,500 EUR needs a Compliance Officer [REF-001]."))
	s := strategyWith(func(s *Strategy) { s.Critics, s.CriticRounds, s.TopK = []string{CriticCitations}, 2, 3 })
	_, run := newTestPipeline(t, s, writer, offlineCritic{}, nil)
	res := run("Who approves a refund above 2,500 EUR?")
	tr := traceOf(res)
	if !strings.Contains(tr["critic_loop#1"], "citations: FAILED") || !strings.Contains(tr["critic_loop#2"], "citations: passed") {
		t.Fatalf("trace = %v", tr)
	}
	if !strings.Contains(res.Final, "2,500 EUR") || strings.Contains(res.Final, "9,999") {
		t.Fatalf("final = %s", res.Final)
	}
	if req := writer.Requests(); len(req) != 2 || !strings.Contains(lastUserText(req[1]), "9999") {
		t.Fatal("the revision request must carry the critic's feedback")
	}
}

func lastUserText(req *model.LLMRequest) string {
	var b strings.Builder
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if p != nil {
				b.WriteString(p.Text)
			}
		}
	}
	return b.String()
}

// Critics run in strategy order and stop at the first failure: the critic
// after a failing one is never called (its fake has no turns and would fail).
func TestCriticOrderMatters(t *testing.T) {
	t.Parallel()
	writer := fakellm.New("writer", fakellm.TextTurn("Above 9,999 EUR [REF-001]."))
	silent := fakellm.New("critic-never-called")
	s := strategyWith(func(s *Strategy) { s.Critics = []string{CriticCitations, CriticCompleteness} })
	_, run := newTestPipeline(t, s, writer, silent, nil)
	res := run("Who approves a refund?")
	if silent.CallCount() != 0 {
		t.Fatalf("completeness was called %d times after citations failed", silent.CallCount())
	}
	if !strings.Contains(res.Final, "критики: НЕ пройшли") {
		t.Fatalf("final = %s", res.Final)
	}
}

func TestBrokenCriticDoesNotApprove(t *testing.T) {
	t.Parallel()
	critic := fakellm.New("critic", fakellm.Turn{Err: errors.New("deadline exceeded")})
	s := strategyWith(func(s *Strategy) { s.Critics = []string{CriticCompleteness} })
	_, run := newTestPipeline(t, s, offlineWriter{}, critic, nil)
	tr := traceOf(run("Who approves a refund?"))
	if !strings.Contains(tr["critic_loop#1"], "completeness: FAILED — [system]") {
		t.Fatalf("a crashed critic must be a rejected verdict: %q", tr["critic_loop#1"])
	}
}

func TestHumanGate(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, decision, want string
	}{
		{"deny", StatusDenied, "ЗВІТ НЕ ВИПУЩЕНО (denied"},
		{"approve", StatusApproved, "затверджено: oksana"},
		{"resume without a ledger decision", "", "ЗВІТ НЕ ВИПУЩЕНО (invalid, system:gate"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ledger := NewLedger(nil)
			pipe, err := NewPipeline(PipelineConfig{Strategy: strategyWith(nil), Lib: testLib, Writer: offlineWriter{},
				Critic: offlineCritic{}, Gate: &Gate{Mode: GateHuman, Ledger: ledger}, Meter: NewMeter()})
			if err != nil {
				t.Fatal(err)
			}
			sess, err := newAskSession(pipe)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			events, err := sess.turn(ctx, genai.NewContentFromText("Who approves a refund above 2,500 EUR?", genai.RoleUser))
			if err != nil {
				t.Fatal(err)
			}
			id, payload, ok := pauseOf(events)
			if !ok || payload["report_hash"] == nil {
				t.Fatal("the gate did not pause with evidence")
			}
			if rec, _ := ledger.Get(id); rec.Status != StatusPending {
				t.Fatalf("ledger before decision: %+v", rec)
			}
			if tt.decision != "" {
				if _, err := ledger.Decide(id, tt.decision, "oksana", "test"); err != nil {
					t.Fatal(err)
				}
			}
			events, err = sess.turn(ctx, resumeMessage(id, "approve")) // the reply word is not trusted
			if err != nil {
				t.Fatal(err)
			}
			final := ""
			for _, ev := range events {
				if s, ok := ev.Output.(string); ok {
					final = s
				}
			}
			if !strings.Contains(final, tt.want) {
				t.Fatalf("final = %q, want %q", final, tt.want)
			}
			if _, err := ledger.Decide(id, StatusApproved, "late", "x"); !errors.Is(err, ErrAlreadyDecided) {
				t.Fatal("a decision must be final")
			}
		})
	}
}

func TestParseDecision(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"approve": StatusApproved, " Так ": StatusApproved, "deny": StatusDenied, "ні": StatusDenied} {
		if got, err := ParseDecision(in); err != nil || got != want {
			t.Errorf("ParseDecision(%q) = %s, %v", in, got, err)
		}
	}
	if _, err := ParseDecision("maybe"); err == nil {
		t.Error("garbage accepted")
	}
}

func TestNewPipelineRejectsIncompleteConfig(t *testing.T) {
	t.Parallel()
	if _, err := NewPipeline(PipelineConfig{Strategy: strategyWith(nil)}); err == nil {
		t.Fatal("incomplete config accepted")
	}
}
