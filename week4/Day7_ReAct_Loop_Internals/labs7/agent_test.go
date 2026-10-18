package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/ledger"
	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/scripted"
)

// Ці тести НЕ на t.Skip: agent.go уже готовий, і вони перевіряють, що
// штатний цикл ADK справді робить те, що каже лекція — без ключа й мережі.
// Модель скриптована, але цикл, виклик інструментів і журнал подій — справжні.

func TestADKAgentRunsTheReActLoopToATicket(t *testing.T) {
	l := ledger.New()
	a, err := NewADKAgent(offlineModel(), l, maxIterations)
	if err != nil {
		t.Fatal(err)
	}
	res, err := labrun.Run(context.Background(), a, defaultTask)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Ефект інструмента — створений тікет, а не текст, який про нього написала модель.
	if got := l.Tickets(); len(got) != 1 || !strings.HasPrefix(got[0], "LDG-1") {
		t.Errorf("tickets = %v, want exactly LDG-1", got)
	}
	for _, name := range []string{"search_internal_wiki", "get_merchant_payouts", "create_jira_ticket"} {
		if !res.CalledTool(name) {
			t.Errorf("the loop never called %s; calls: %v", name, res.ToolCalls)
		}
	}
	// Кожен FunctionResponse несе CallID свого виклику — так рушій зіставляє
	// результати, коли в одному кроці кілька викликів.
	ids := map[string]bool{}
	for _, fr := range res.ToolResults {
		ids[fr.ID] = true
	}
	for _, id := range []string{"c1", "c2", "c3"} {
		if !ids[id] {
			t.Errorf("no observation for call %s; got %v", id, ids)
		}
	}
}

func TestADKAgentStopsAtTheStepCap(t *testing.T) {
	// Модель, що ніколи не відповідає без tool call. Flow.Run сам по собі —
	// `for {}`: без BeforeModelCallback цей тест не завершився б.
	endless := scripted.Forever("endless", scripted.Call(100,
		scripted.FC("", "search_internal_wiki", map[string]any{"query": "again"})))
	a, err := NewADKAgent(endless, ledger.New(), 3)
	if err != nil {
		t.Fatal(err)
	}
	_, err = labrun.Run(context.Background(), a, defaultTask)
	if !errors.Is(err, ErrMaxIterations) {
		t.Fatalf("err = %v; want errors.Is(err, ErrMaxIterations)", err)
	}
	if n := endless.Calls(); n != 3 {
		t.Errorf("model was called %d times; the cap of 3 must stop it BEFORE a 4th call", n)
	}
}

func TestRunAgentSaysWhenItIsOffline(t *testing.T) {
	t.Setenv("MODEL", "")
	t.Setenv("DEFAULT_MODEL_PROVIDER", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Chdir(t.TempDir()) // жодного .env поруч

	var out bytes.Buffer
	if err := runAgent(context.Background(), &out, ""); err != nil {
		t.Fatalf("runAgent: %v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{"модель: офлайн", "НЕ відповідь моделі", "act [c1] search_internal_wiki", "obs [c3] create_jira_ticket: LDG-1", "tickets: [LDG-1"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q\n%s", want, got)
		}
	}
}
