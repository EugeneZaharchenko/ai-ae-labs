// B3 · Loop — repeat until a KNOWN exit test passes. The back-edge is a
// graph edge that carries a route; the iteration cap is written by you.
//
//	Start → submit → poll ─BoolRoute(true)──→ report (LlmAgent)
//	                  ↑  └─BoolRoute(false)─→ wait ─┐
//	                  └─────────────────────────────┘
//
// ADK catches half of the mistake: a cycle whose edges are ALL unconditional
// fails at build time with workflow.ErrUnconditionalCycle. A routed cycle
// without a counter still builds — maxPolls below is the other half.
//
//	go run .                                  # job finishes on poll 3
//	go run . -input "render video, needs 9"   # hits the cap
//	go run . -live
package main

import (
	"fmt"
	"regexp"
	"strconv"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:    "B3",
	Title: "Loop",
	Input: "render report, needs 3 polls",
	Build: build,
}

func main() { kit.Main(spec) }

// maxPolls is the hard cap. The exit test (job done) is the real stop; the
// cap only bounds the bill when the test never passes.
const maxPolls = 5

// job is the state that travels around the loop.
type job struct {
	ID     string `json:"id"`
	Needs  int    `json:"needs"`
	Polls  int    `json:"polls"`
	Status string `json:"status"`
}

var numRE = regexp.MustCompile(`\d+`)

// submit starts a simulated job; the last number in the request says how
// many polls it takes to finish (default 3).
func submit(_ agent.Context, req string) (job, error) {
	needs := 3
	if nums := numRE.FindAllString(req, -1); len(nums) > 0 {
		needs, _ = strconv.Atoi(nums[len(nums)-1])
	}
	return job{ID: "job-1", Needs: needs, Status: "running"}, nil
}

// poll checks the job once and routes: true leaves the loop, false goes
// around again. It is the only place that decides — deterministic code.
func poll(ctx agent.Context, j job) (*session.Event, error) {
	j.Polls++
	done := false
	switch {
	case j.Polls >= j.Needs:
		j.Status, done = "done", true
	case j.Polls >= maxPolls:
		j.Status, done = "gave up: poll cap reached", true
	}
	ev := session.NewEvent(ctx, ctx.InvocationID())
	ev.Output = j
	ev.Routes = []string{fmt.Sprint(workflow.BoolRoute(done))}
	return ev, nil
}

// wait stands in for a backoff sleep between polls.
func wait(_ agent.Context, j job) (job, error) { return j, nil }

func reportBrain(p kit.Prompt) kit.Reply {
	if p.Has(`"status":"done"`) {
		return kit.Say("Your job finished. (%s)", p.Text)
	}
	return kit.Say("The job did not finish in time; we stopped polling. (%s)", p.Text)
}

func build(m kit.Models) (agent.Agent, error) {
	reporter, err := llmagent.New(llmagent.Config{
		Name:        "report",
		Description: "Tells the user how the job ended.",
		Model:       m.For("report", reportBrain),
		Instruction: `You get a job record as JSON (id, needs, polls, status).
Tell the user in one sentence whether the job finished and after how many polls.`,
	})
	if err != nil {
		return nil, err
	}
	report, err := workflow.NewAgentNode(reporter, workflow.NodeConfig{})
	if err != nil {
		return nil, err
	}
	pollNode := workflow.NewFunctionNode("poll", poll, workflow.NodeConfig{})
	waitNode := workflow.NewFunctionNode("wait", wait, workflow.NodeConfig{})

	submitNode := workflow.NewFunctionNode("submit", submit, workflow.NodeConfig{})

	edges := workflow.NewEdgeBuilder().
		Add(workflow.Start, submitNode).
		Add(submitNode, pollNode).
		AddRoute(pollNode, report, workflow.BoolRoute(true)).
		AddRoute(pollNode, waitNode, workflow.BoolRoute(false)).
		// The back-edge is a Default route, not a plain Add: poll already has
		// one unconditional incoming edge (from submit), and a second one
		// would be a fan-in without a JoinNode (ErrUnsupportedFanIn).
		// Default counts as conditional, so this is legal.
		AddRoute(waitNode, pollNode, workflow.Default).
		Build()

	return workflowagent.New(workflowagent.Config{
		Name:        "poll_loop",
		Description: "Poll a job until done, with a hard cap.",
		Edges:       edges,
	})
}

// unconditionalCycle builds the same loop WITHOUT routes. It never gets to
// run: validation rejects it with workflow.ErrUnconditionalCycle.
func unconditionalCycle() error {
	p := workflow.NewFunctionNode("poll", func(_ agent.Context, in any) (any, error) { return in, nil }, workflow.NodeConfig{})
	w := workflow.NewFunctionNode("wait", func(_ agent.Context, in any) (any, error) { return in, nil }, workflow.NodeConfig{})
	_, err := workflow.New("forever", workflow.Chain(workflow.Start, p, w, p))
	return err
}
