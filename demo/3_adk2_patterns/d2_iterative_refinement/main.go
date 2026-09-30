// D2 · Iterative Refinement — D1 in a loop: generate, score with a
// DETERMINISTIC checker, feed the gaps back, stop at the threshold or at the
// iteration cap. Keep the BEST draft, not the last one: refinement regresses.
//
//	Start → refine (DynamicNode: for i < maxIterations { RunNode(generate); score }) → best draft
//
//	go run .            # offline: 50 → 75 → 50 (regression) → best = attempt 2
//	go run . -live
//	go run . console
package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:    "D2",
	Title: "Iterative Refinement",
	Input: "Write a product blurb for our trail jacket",
	Build: build,
}

func main() { kit.Main(spec) }

const (
	// maxIterations is the hard cap. A loop without one is the classic
	// unbounded-cost incident; the graph validator will not catch it for you.
	maxIterations = 3
	// threshold is the score at which refinement stops early.
	threshold = 100
	// maxWords is the length budget the checker enforces.
	maxWords = 20
)

// required are the facts the blurb must carry.
var required = []string{"waterproof", "breathable", "€129"}

// score is the deterministic checker: 25 points per required fact plus 25
// for staying within budget. It returns the gaps as feedback for the writer.
func score(draft string) (int, []string) {
	low := strings.ToLower(draft)
	pts := 0
	var gaps []string
	for _, r := range required {
		if strings.Contains(low, r) {
			pts += 25
		} else {
			gaps = append(gaps, "mention "+r)
		}
	}
	if n := len(strings.Fields(draft)); n <= maxWords {
		pts += 25
	} else {
		gaps = append(gaps, fmt.Sprintf("cut to %d words (now %d)", maxWords, n))
	}
	return pts, gaps
}

// attempt is one pass of the loop, kept for the report.
type attempt struct {
	N     int    `json:"n"`
	Score int    `json:"score"`
	Draft string `json:"draft"`
}

// result is the output of the refine node.
type result struct {
	Best     attempt   `json:"best"`
	Attempts []attempt `json:"attempts"`
	Stop     string    `json:"stop"`
}

// refine is the loop itself, independent of ADK so it can be tested with a
// plain function as the generator.
func refine(brief string, generate func(prompt string) (string, error)) (result, error) {
	var res result
	best := attempt{Score: -1}
	var gaps []string
	for i := 1; i <= maxIterations; i++ {
		prompt := fmt.Sprintf("%s\nAttempt %d/%d.", brief, i, maxIterations)
		if len(gaps) > 0 {
			prompt += "\nFix: " + strings.Join(gaps, "; ") + "."
		}
		draft, err := generate(prompt)
		if err != nil {
			return res, fmt.Errorf("attempt %d: %w", i, err)
		}
		s, g := score(draft)
		gaps = g
		a := attempt{N: i, Score: s, Draft: strings.TrimSpace(draft)}
		res.Attempts = append(res.Attempts, a)
		if s > best.Score {
			best = a
		}
		if s >= threshold {
			res.Best, res.Stop = best, fmt.Sprintf("threshold %d reached", threshold)
			return res, nil
		}
	}
	res.Best, res.Stop = best, fmt.Sprintf("cap of %d iterations; kept best, not last", maxIterations)
	return res, nil
}

var attemptRe = regexp.MustCompile(`Attempt (\d+)/`)

// writerBrain improves for two passes and then regresses — the behaviour
// that makes "keep the best" worth its one line of code.
func writerBrain(p kit.Prompt) kit.Reply {
	n := 1
	if m := attemptRe.FindStringSubmatch(p.Text); m != nil {
		n, _ = strconv.Atoi(m[1])
	}
	switch n {
	case 1:
		return kit.Say("A jacket for hikers. Waterproof.")
	case 2:
		return kit.Say("Waterproof, breathable trail jacket for hikers.")
	default:
		return kit.Say("Waterproof jacket for only €129 — the one jacket every hiker, climber and city walker will love in rain, wind, snow and sun.")
	}
}

func build(m kit.Models) (agent.Agent, error) {
	writer, err := llmagent.New(llmagent.Config{
		Name:        "writer",
		Description: "Writes a product blurb.",
		Model:       m.For("writer", writerBrain),
		Instruction: "Write a product blurb for a trail jacket. Apply every item after 'Fix:'. Reply with the blurb only.",
	})
	if err != nil {
		return nil, err
	}
	writerNode, err := workflow.NewAgentNode(writer, workflow.NodeConfig{})
	if err != nil {
		return nil, err
	}

	loop := workflow.NewDynamicNode("refine",
		func(ctx agent.Context, brief string, _ func(*session.Event) error) (result, error) {
			return refine(brief, func(prompt string) (string, error) {
				return workflow.RunNode[string](ctx, writerNode, prompt)
			})
		},
		workflow.NodeConfig{},
	)

	report := workflow.NewFunctionNode("report", func(_ agent.Context, r result) (string, error) {
		var b strings.Builder
		for _, a := range r.Attempts {
			fmt.Fprintf(&b, "attempt %d: score %d; ", a.N, a.Score)
		}
		fmt.Fprintf(&b, "stop: %s → best #%d (%d): %s", r.Stop, r.Best.N, r.Best.Score, r.Best.Draft)
		return b.String(), nil
	}, workflow.NodeConfig{})

	return workflowagent.New(workflowagent.Config{
		Name:        "blurb_refinement",
		Description: "Generate → deterministic score → refine, capped, best kept.",
		Edges:       workflow.Chain(workflow.Start, loop, report),
	})
}
