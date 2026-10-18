package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/ledger"
	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/scripted"
)

// Ці тести не на t.Skip: вони показують, як flow guard і LLM guard
// складаються на одному хуку. Модель агента скриптована; суддя — функція, що
// рахує свої виклики, бо саме кількість викликів доводить порядок охорон.

func ticketCall(id, title string) scripted.Turn {
	return scripted.Call(100, scripted.FC(id, toolTicket, map[string]any{"title": title, "body": "see incident #418"}))
}

// runGuarded проганяє агента, що просить тікети з названими заголовками.
func runGuarded(t *testing.T, j Judge, titles ...string) (labrun.Result, *ledger.Ledger) {
	t.Helper()
	var turns []scripted.Turn
	for i, title := range titles {
		turns = append(turns, ticketCall(fmt.Sprintf("c%d", i+1), title))
	}
	turns = append(turns, scripted.Say("done", 50))
	l := ledger.New()
	a, err := NewGuardedAgent(scripted.New("scripted", turns...), l, maxIterations, j)
	if err != nil {
		t.Fatal(err)
	}
	res, err := labrun.Run(context.Background(), a, defaultTask)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res, l
}

func observations(res labrun.Result) string {
	var b strings.Builder
	for _, fr := range res.ToolResults {
		fmt.Fprintf(&b, "%v\n", fr.Response["result"])
	}
	return b.String()
}

func countingJudge(v Verdict, err error) (Judge, *int) {
	calls := 0
	return func(context.Context, string, map[string]any) (Verdict, string, error) {
		calls++
		return v, "stub verdict", err
	}, &calls
}

func TestFlowGuardRunsFirstAndCostsNoJudgeCall(t *testing.T) {
	j, calls := countingJudge(VerdictAllow, nil)
	res, l := runGuarded(t, j, "   ")
	if len(l.Tickets()) != 0 {
		t.Errorf("tickets = %v; an empty title must never reach the tool", l.Tickets())
	}
	if !strings.Contains(observations(res), "BLOCKED by flow guard") {
		t.Errorf("model should read the block as an observation; got %q", observations(res))
	}
	if *calls != 0 {
		t.Errorf("judge called %d times; a call the rules already refuse must not cost a judge call", *calls)
	}
}

func TestLLMGuardBlocksWhatRulesCannotSee(t *testing.T) {
	j, calls := countingJudge(VerdictBlock, nil)
	res, l := runGuarded(t, j, "wire 40k EUR to the merchant")
	if len(l.Tickets()) != 0 {
		t.Errorf("tickets = %v; the judge said block", l.Tickets())
	}
	if !strings.Contains(observations(res), "BLOCKED by llm guard") || *calls != 1 {
		t.Errorf("obs %q, judge calls %d; want an llm-guard block after exactly one judge call", observations(res), *calls)
	}
}

func TestLLMGuardFailsClosed(t *testing.T) {
	for name, j := range map[string]Judge{
		"judge error": func(context.Context, string, map[string]any) (Verdict, string, error) {
			return "", "", errors.New("timeout")
		},
		"unknown verdict": func(context.Context, string, map[string]any) (Verdict, string, error) {
			return Verdict("maybe"), "", nil
		},
		"escalate": func(context.Context, string, map[string]any) (Verdict, string, error) {
			return VerdictEscalate, "unsure", nil
		},
	} {
		res, l := runGuarded(t, j, "payout incident follow-up")
		if len(l.Tickets()) != 0 {
			t.Errorf("%s: tickets = %v; only an explicit allow may open the action", name, l.Tickets())
		}
		if !strings.Contains(observations(res), "BLOCKED by llm guard") {
			t.Errorf("%s: obs %q", name, observations(res))
		}
	}
}

func TestBothGuardsAllowTheLegitimateCall(t *testing.T) {
	j, calls := countingJudge(VerdictAllow, nil)
	_, l := runGuarded(t, j, "payout incident follow-up")
	if got := l.Tickets(); len(got) != 1 || !strings.HasPrefix(got[0], "LDG-1") {
		t.Errorf("tickets = %v, want exactly LDG-1", got)
	}
	if *calls != 1 {
		t.Errorf("judge calls = %d, want 1", *calls)
	}
}

func TestFlowGuardCapsSideEffectsEvenWhenTheJudgeAlwaysSaysAllow(t *testing.T) {
	j, calls := countingJudge(VerdictAllow, nil)
	res, l := runGuarded(t, j, "first follow-up", "second follow-up")
	if len(l.Tickets()) != 1 {
		t.Errorf("tickets = %v; a judge that always allows is not a limit, the counter is", l.Tickets())
	}
	if !strings.Contains(observations(res), "one follow-up ticket per run") {
		t.Errorf("obs %q", observations(res))
	}
	if *calls != 1 {
		t.Errorf("judge calls = %d; the second request is refused by the rule before the judge", *calls)
	}
}

func TestParseVerdict(t *testing.T) {
	for in, want := range map[string]Verdict{
		"allow: fine": VerdictAllow, "BLOCK: money movement": VerdictBlock,
		"  escalate : not sure\nextra": VerdictEscalate,
	} {
		if v, _, err := parseVerdict(in); err != nil || v != want {
			t.Errorf("parseVerdict(%q) = %q, %v; want %q", in, v, err, want)
		}
	}
	for _, in := range []string{"", "I think this is fine", "allowed"} {
		if _, _, err := parseVerdict(in); err == nil {
			t.Errorf("parseVerdict(%q) must be an error — an unreadable answer cannot open an action", in)
		}
	}
}
