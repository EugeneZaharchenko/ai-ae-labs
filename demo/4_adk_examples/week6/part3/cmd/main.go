// Week 6, part 3 — a multi-agent panel with the narrowest possible channel.
//
// Three specialist agents review the same refund function in parallel. They do
// not talk to each other and they do not "negotiate" in free text: each ends
// its turn with one line, `VERDICT <role> PASS|FAIL <reason>`, and the program
// — not another model — merges the lines. The merge rule is plain Go:
// any FAIL, or any missing/garbled verdict, rejects the candidate.
//
//	go run .          # offline: scripted specialists, no key, no network
//	go run . -sabotage # one specialist returns prose instead of a verdict
//
// This is the "shared graph state" channel from Day 12 slide 9b, shown with
// ADK's parallelagent. Parallel agents run isolated; the only thing that
// crosses between them is what the merge function chooses to read.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagents/parallelagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/scripted"
)

var roles = []string{"correctness", "security", "style"}

func main() { os.Exit(runCLI(os.Args[1:], os.Stdout)) }

// runCLI exit codes: 0 panel accepted; 1 panel rejected or run error; 2 bad flags.
func runCLI(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("week6-part3", flag.ContinueOnError)
	sabotage := fs.Bool("sabotage", false, "make the style specialist answer in prose instead of a verdict")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	verdicts, err := runPanel(context.Background(), panelModels(*sabotage), "func Refund(cents int64, pct int) int64")
	if err != nil {
		fmt.Fprintln(out, "run error:", err)
		return 1
	}
	accepted, why := Merge(verdicts)
	for _, r := range roles {
		fmt.Fprintf(out, "· %-12s %s\n", r, verdicts[r])
	}
	fmt.Fprintf(out, "panel: accepted=%v (%s)\n", accepted, why)
	if !accepted {
		return 1
	}
	return 0
}

// panelModels scripts one model per specialist. It is a test double: it proves
// the wiring and the merge rule, not the quality of any review.
func panelModels(sabotage bool) map[string]model.LLM {
	style := "VERDICT style PASS names follow the package conventions"
	if sabotage {
		style = "Looks fine to me, maybe rename a variable."
	}
	return map[string]model.LLM{
		"correctness": scripted.New("correctness", scripted.Say("VERDICT correctness PASS integer cents, no float64", 40)),
		"security":    scripted.New("security", scripted.Say("VERDICT security PASS no external input reaches a query", 40)),
		"style":       scripted.New("style", scripted.Say(style, 40)),
	}
}

// runPanel runs the specialists in parallel and returns each role's final text.
func runPanel(ctx context.Context, models map[string]model.LLM, candidate string) (map[string]string, error) {
	var subs []agent.Agent
	for _, r := range roles {
		a, err := llmagent.New(llmagent.Config{
			Name:        r + "_critic",
			Description: "Reviews the candidate from the " + r + " angle.",
			Model:       models[r],
			Instruction: "Review the candidate. Reply with exactly one line: VERDICT " + r + " PASS|FAIL <reason>.",
		})
		if err != nil {
			return nil, err
		}
		subs = append(subs, a)
	}
	panel, err := parallelagent.New(parallelagent.Config{
		AgentConfig: agent.Config{Name: "review_panel", SubAgents: subs},
	})
	if err != nil {
		return nil, err
	}
	r, err := runner.New(runner.Config{AppName: "week6-part3", Agent: panel, SessionService: session.InMemoryService(), AutoCreateSession: true})
	if err != nil {
		return nil, err
	}
	got := map[string]string{}
	msg := genai.NewContentFromText(candidate, genai.RoleUser)
	for ev, runErr := range r.Run(ctx, "u", "s", msg, agent.RunConfig{}) {
		if runErr != nil {
			return nil, runErr
		}
		if ev == nil || ev.LLMResponse.Content == nil || !strings.HasSuffix(ev.Author, "_critic") {
			continue
		}
		role := strings.TrimSuffix(ev.Author, "_critic")
		for _, p := range ev.LLMResponse.Content.Parts {
			if p.Text != "" {
				got[role] = strings.TrimSpace(p.Text)
			}
		}
	}
	return got, nil
}

var verdictRe = regexp.MustCompile(`^VERDICT (\w+) (PASS|FAIL) (.+)$`)

// Merge is the whole channel contract: a verdict must parse, carry the role of
// the agent that produced it, and PASS. Anything else rejects — a garbled
// answer is not a vote for "probably fine".
func Merge(verdicts map[string]string) (bool, string) {
	var bad []string
	for _, r := range roles {
		m := verdictRe.FindStringSubmatch(verdicts[r])
		switch {
		case m == nil:
			bad = append(bad, r+": missing or unparseable verdict")
		case m[1] != r:
			bad = append(bad, r+": verdict claims role "+m[1])
		case m[2] == "FAIL":
			bad = append(bad, r+": "+m[3])
		}
	}
	if len(bad) == 0 {
		return true, "3/3 structured PASS"
	}
	sort.Strings(bad)
	return false, strings.Join(bad, "; ")
}
