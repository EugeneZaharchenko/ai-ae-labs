package offline

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week6/Day12_Self_Improving_Agents_Demo_Day/labs12/internal/ledgerkb"
)

func run(t *testing.T, instr string, toolNames []string, q string) labrun.Result {
	t.Helper()
	tools, err := ledgerkb.New().Tools(toolNames)
	if err != nil {
		t.Fatal(err)
	}
	a, err := llmagent.New(llmagent.Config{
		Name: "w", Model: Worker{}, Tools: tools,
		InstructionProvider: func(agent.ReadonlyContext) (string, error) { return instr, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := labrun.Run(context.Background(), a, q)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestWorkerBehaviourFollowsTheInstruction(t *testing.T) {
	t.Parallel()
	d1 := ledgerkb.Dev()[1] // chargeback question
	all := ledgerkb.AllToolNames()

	cases := []struct {
		name      string
		instr     string
		tools     []string
		wantCalls int
		wantFound int
	}{
		{"snippet only", "Search once.", all, 1, 0},
		{"figures from snippets are still nothing", "Quote every figure.", all, 1, 0},
		{"reads one document", "Use kb_read on the top hit. Quote every figure.", all, 2, 3},
		{"reads three documents", "Use kb_read. Check every relevant document. Quote every figure.", all, 4, 3},
		{"kb_read not granted", "Use kb_read. Quote every figure.", []string{"kb_search"}, 1, 0},
		{"read but summarised", "Use kb_read.", all, 2, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			res := run(t, c.instr, c.tools, d1.Question)
			if len(res.ToolCalls) != c.wantCalls {
				t.Errorf("tool calls = %v, want %d", res.ToolCalls, c.wantCalls)
			}
			if found, _ := ledgerkb.Score(res.Final, d1.Facts); found != c.wantFound {
				t.Errorf("found %d facts, want %d; answer: %q", found, c.wantFound, res.Final)
			}
		})
	}
}

func TestNoMatchAnswersHonestly(t *testing.T) {
	t.Parallel()
	res := run(t, "Use kb_read.", ledgerkb.AllToolNames(), "zzzz qqqq")
	if !strings.Contains(res.Final, "No matching policy") {
		t.Errorf("answer = %q", res.Final)
	}
}

func TestNameSaysItIsNotAnLLM(t *testing.T) {
	t.Parallel()
	if !strings.Contains(Worker{}.Name(), "NOT an LLM") {
		t.Error("the offline model must announce itself")
	}
}
