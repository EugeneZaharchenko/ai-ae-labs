package main

import (
	"context"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/ledger"
)

// Ці тести не на t.Skip: team.go уже готовий. Моделі скриптовані (по одній на
// вузол), але граф, fan-out/join, цикл кожного llmagent, виклики інструментів і
// журнал подій — справжні.

func TestTeamRunsAReActLoopInsideEveryNode(t *testing.T) {
	ms, models := offlineTeam()
	l := ledger.New()
	a, err := NewTeamAgent(ms, l, maxIterations, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := labrun.Run(context.Background(), a, defaultTask)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Кожен вузол — свій інструмент: інструмент дослідника не доступний reporter-у й навпаки.
	for _, name := range []string{"search_internal_wiki", "get_merchant_payouts", "create_jira_ticket"} {
		if !res.CalledTool(name) {
			t.Errorf("tool %s was never called; calls: %v", name, res.ToolCalls)
		}
	}
	if got := l.Tickets(); len(got) != 1 || !strings.HasPrefix(got[0], "LDG-1") {
		t.Errorf("tickets = %v, want exactly LDG-1", got)
	}
	// ReAct усередині вузла: кожна модель кликана двічі — Reason→Act, потім
	// Observation→відповідь. Граф не знає про ці дві ітерації; їх крутить Flow.Run.
	for _, m := range models {
		if n := m.Calls(); n != 2 {
			t.Errorf("model %s called %d times, want 2 (one tool round-trip per node)", m.Name(), n)
		}
	}
	// У журналі подій видно всі три вузли як окремих авторів.
	authors := map[string]bool{}
	for _, e := range res.Events {
		authors[e.Author] = true
	}
	for _, node := range []string{nodeWiki, nodePayouts, nodeReporter} {
		if !authors[node] {
			t.Errorf("no events authored by %s; authors: %v", node, authors)
		}
	}
}

func TestTeamGuardsOnlyTheNodeThatWrites(t *testing.T) {
	ms, _ := offlineTeam()
	l := ledger.New()
	block := func(context.Context, string, map[string]any) (Verdict, string, error) {
		return VerdictBlock, "stub verdict", nil
	}
	a, err := NewTeamAgent(ms, l, maxIterations, block)
	if err != nil {
		t.Fatal(err)
	}
	res, err := labrun.Run(context.Background(), a, defaultTask)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(l.Tickets()) != 0 {
		t.Errorf("tickets = %v; the judge blocked the only write", l.Tickets())
	}
	// Читання не гейтяться: обидва дослідники відпрацювали повністю.
	if !res.CalledTool("search_internal_wiki") || !res.CalledTool("get_merchant_payouts") {
		t.Errorf("read-only nodes must not be gated; calls: %v", res.ToolCalls)
	}
}
