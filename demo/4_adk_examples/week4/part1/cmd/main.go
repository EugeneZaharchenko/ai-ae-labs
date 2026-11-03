// Week 4, part 1 — the ReAct loop, written by hand.
//
// The lecture's claim is that an agent loop is four things: call the model,
// branch on whether it asked for a tool, feed the results back, and stop. This
// example is that claim in Go, over the LEDGERWORKS payout desk the labs use
// from Day 7 onward.
//
// Run it against a real model:
//
//	go run . -task "Merchant A-114 says they have not been paid. Find out why."
//
// and against no provider at all:
//
//	go run . -offline
//
// The second form is not a stub: the loop, the parallel tool dispatch, the
// one-side-effect guard and the step cap all run for real. Only the model is a
// script, which is exactly the substitution the tests make too.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/internal/adkrun"
	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/internal/scripted"
	"github.com/dimetron/ai-eng-course/labs/internal/usage"
)

func main() { os.Exit(runCLI(os.Args[1:])) }

func runCLI(args []string) int {
	fs := flag.NewFlagSet("week4-part1", flag.ContinueOnError)
	task := fs.String("task", defaultTask, "the incident to investigate")
	offline := fs.Bool("offline", false, "replay a scripted investigation instead of calling a provider")
	priceIn := fs.Float64("price-in", 0, "USD per million input tokens, if you want the run costed")
	priceOut := fs.Float64("price-out", 0, "USD per million output tokens")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	llm, label := resolveModel(*offline)

	// The world and the meter outlive the loop on purpose: the ticket the agent
	// opened and the tokens it spent are the two things worth asserting on, and
	// neither is in the text it returned.
	world := &World{}
	meter := &usage.Meter{}
	inv := &investigator{model: llm, world: world, meter: meter}

	// The hand-written loop wears the same clothes as any other node: a dynamic
	// node inside a one-node workflow. That is the bridge to the rest of the
	// week — same loop, but its progress and result now travel as events.
	node := workflow.NewDynamicNode[string, string]("react_loop", inv.run, adkrun.NodeConfig())
	graph, err := adkrun.Pipeline("week4_react_loop",
		"A ReAct loop written by hand, hosted by an ADK workflow.", node)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build graph: %v\n", err)
		return 1
	}

	var price *usage.Price
	if *priceIn > 0 || *priceOut > 0 {
		price = &usage.Price{InPerM: *priceIn, OutPerM: *priceOut}
	}

	fmt.Println("week 4, part 1 — the ReAct loop, by hand")
	fmt.Printf("%s\n", label)
	fmt.Printf("task:  %s\n\n", *task)

	answer, runErr := adkrun.Run(context.Background(), graph, *task, os.Stdout)

	// Report what happened even when the run failed. A crash that also loses the
	// token count is the failure mode that hides a runaway loop.
	fmt.Println()
	if answer != "" {
		fmt.Printf("answer: %s\n", answer)
	}
	for _, t := range world.OpenTickets() {
		fmt.Printf("ticket: %s — %s\n", t.Key, t.Summary)
	}
	fmt.Println(meter.Total().Line(price))
	fmt.Println(usage.PerTool(inv.Counts()))

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", runErr)
		if errors.Is(runErr, ErrStepCap) {
			fmt.Fprintf(os.Stderr, "the step cap fired, which means the model never stopped asking for tools\n")
		}
		return 1
	}
	return 0
}

// resolveModel picks the model the loop talks to.
//
// A real provider is the default, because the whole point of this module is a
// real agent. A missing key is not a crash: the example falls back to a scripted
// replay and says so in the startup line, so a learner with no key still watches
// the loop, the parallel dispatch and the stop rule work.
func resolveModel(offline bool) (model.LLM, string) {
	if offline {
		return scriptedInvestigation(), "scripted replay — no provider was called"
	}
	if err := modelcfg.LoadEnv("."); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	llm, choice, err := modelcfg.Load(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "no model provider: %v\n", err)
		fmt.Fprintln(os.Stderr, "falling back to a scripted replay — pass -offline to skip this probe")
		return scriptedInvestigation(), "scripted replay — no provider was called"
	}
	return llm, choice.Reason
}

// scriptedInvestigation replays, step for step, what a competent model does
// here: read the merchant's payouts, read the held batch and the merchant
// profile together, look up the incident the hold names, open the ticket, answer.
//
// The chaining is the point. No single tool holds the answer — the payouts name
// a batch, the batch names an incident, and only the wiki explains it — so a
// script has to encode the same four dependent steps a real model would reason
// its way to. If the loop's plumbing is wrong, this fails exactly where the real
// run would.
func scriptedInvestigation() *scripted.Model {
	return scripted.New("scripted",
		scripted.Call(1_450,
			scripted.FC("s1", "get_merchant_payouts", map[string]any{"merchant_id": "A-114"})),

		scripted.Call(1_820,
			scripted.FC("s2", "get_settlement_batch", map[string]any{"batch_id": "422"}),
			scripted.FC("s3", "get_merchant", map[string]any{"merchant_id": "A-114"})),

		scripted.Call(1_610,
			scripted.FC("s4", "search_internal_wiki", map[string]any{"query": "held batch cutoff legacy payouts"})),

		scripted.Call(2_040,
			scripted.FC("s5", "create_jira_ticket", map[string]any{
				"summary": "A-114 payouts held: batches #422 and #421 blocked by INC-418 cutoff bug",
				"detail": "Merchant A-114 (Kvitka Market) is on legacy-v1, which reported its batch cutoff " +
					"in local time while the settlement scheduler read UTC, so batches #422 and #421 were " +
					"held for risk review INC-418 instead of being cut off. Payouts P-8891 (EUR 18240.00) " +
					"and P-8892 (EUR 9115.50) are pending. Fix: close INC-418, re-queue both batches from " +
					"the payouts-v3 console per RB-12, then migrate A-114 to payouts-v3 per RB-07.",
			})),

		scripted.Say("A-114 was not paid because batches #422 and #421 are held for risk review INC-418: "+
			"the legacy payout API reported its cutoff in local time while the scheduler read UTC, so "+
			"the batches were never cut off. Close INC-418, re-queue both batches from the payouts-v3 "+
			"console, and migrate A-114 off legacy-v1.", 1_240),
	)
}
