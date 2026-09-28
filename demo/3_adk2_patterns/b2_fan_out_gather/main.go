// B2 · Parallel Fan-Out / Gather — independent sub-tasks run at the same
// time; their outputs merge in ONE place, a JoinNode.
//
//	                ┌→ pricing ────┐
//	Start → topic ──┼→ reviews ────┼→ gather (JoinNode) → merge (fn) → verdict (LlmAgent)
//	                └→ competitors ┘
//
// Two guards, only one mechanical: a fan-in without a JoinNode does not
// build (ErrUnsupportedFanIn); the concurrency cap is your discipline —
// workflow.WithMaxConcurrency(n) set before the first fan-out.
//
//	go run .
//	go run . -live
//	go run . console
package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:    "B2",
	Title: "Parallel Fan-Out / Gather",
	Input: "Should we buy the Acme X1 e-bike?",
	Build: build,
}

func main() { kit.Main(spec) }

// maxConcurrency caps how many branches run at once. Set it before the first
// fan-out: a rate-limit storm under load is a predictable failure.
const maxConcurrency = 2

// latency simulates one slow, independent API round-trip per branch.
const latency = 60 * time.Millisecond

// gauge records the peak number of branches running at the same time, so a
// test can prove the cap holds.
type gauge struct {
	mu        sync.Mutex
	now, peak int
}

func (g *gauge) enter() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.now++
	g.peak = max(g.peak, g.now)
}

func (g *gauge) leave() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.now--
}

func (g *gauge) Peak() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.peak
}

// research builds one independent branch that returns a canned finding.
func research(name, finding string, g *gauge) workflow.Node {
	return workflow.NewFunctionNode(name, func(_ agent.Context, topic string) (string, error) {
		g.enter()
		defer g.leave()
		time.Sleep(latency)
		return fmt.Sprintf("%s: %s", topic, finding), nil
	}, workflow.NodeConfig{})
}

func topic(_ agent.Context, q string) (string, error) {
	q = strings.TrimSpace(strings.TrimSuffix(q, "?"))
	for _, p := range []string{"Should we buy the ", "Should we buy "} {
		q = strings.TrimPrefix(q, p)
	}
	return q, nil
}

// merge is where the branches meet. The JoinNode hands over a map keyed by
// predecessor name; sorting the keys makes the merged text deterministic.
func merge(_ agent.Context, in map[string]any) (string, error) {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "- %s → %v\n", k, in[k])
	}
	return strings.TrimSpace(b.String()), nil
}

// verdictBrain is the offline decision over the merged findings: the one
// place where conflicting branches get reconciled.
func verdictBrain(p kit.Prompt) kit.Reply {
	if p.Has("cheaper alternative") && p.Has("4.6") {
		return kit.Say("VERDICT: buy — strong reviews outweigh the price; revisit if the cheaper rival adds a warranty.")
	}
	return kit.Say("VERDICT: need more data — findings: %d lines.", strings.Count(p.Text, "\n")+1)
}

func build(m kit.Models) (agent.Agent, error) {
	a, _, err := newGraph(m, maxConcurrency)
	return a, err
}

// newGraph wires the fan-out and returns the gauge for tests.
func newGraph(m kit.Models, limit int) (agent.Agent, *gauge, error) {
	g := &gauge{}
	verdictAgent, err := llmagent.New(llmagent.Config{
		Name:        "verdict",
		Description: "Reconciles parallel research into one recommendation.",
		Model:       m.For("verdict", verdictBrain),
		Instruction: `You get findings from three independent research branches (pricing, reviews, competitors).
They may disagree. Reply with one line "VERDICT: <buy | skip | need more data> — <why>".`,
	})
	if err != nil {
		return nil, nil, err
	}
	verdict, err := workflow.NewAgentNode(verdictAgent, workflow.NodeConfig{})
	if err != nil {
		return nil, nil, err
	}

	topicNode := workflow.NewFunctionNode("topic", topic, workflow.NodeConfig{})
	pricing := research("pricing", "1 890 EUR, 12% above segment median", g)
	reviews := research("reviews", "4.6/5 from 1 200 owners; battery praised", g)
	competitors := research("competitors", "a cheaper alternative exists (Volt V2, 1 490 EUR)", g)
	gather := workflow.NewJoinNode("gather")
	mergeNode := workflow.NewFunctionNode("merge", merge, workflow.NodeConfig{})

	edges := workflow.NewEdgeBuilder().
		Add(workflow.Start, topicNode).
		AddFanOut(topicNode, pricing, reviews, competitors).
		AddFanIn(gather, pricing, reviews, competitors).
		Add(gather, mergeNode).
		Add(mergeNode, verdict).
		Build()

	// workflowagent.Config has no concurrency option in v2.4.0, so the graph
	// is built with workflow.New and wrapped in a plain agent: Workflow.Run
	// already has the agent.Config.Run signature.
	w, err := workflow.New("research_fanout", edges, workflow.WithMaxConcurrency(limit))
	if err != nil {
		return nil, nil, err
	}
	a, err := agent.New(agent.Config{
		Name:        "research_fanout",
		Description: "Three independent research branches, merged through a JoinNode.",
		Run:         w.Run,
	})
	return a, g, err
}
