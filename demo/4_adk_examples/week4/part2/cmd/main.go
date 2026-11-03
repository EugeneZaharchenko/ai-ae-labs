// Week 4, part 2 — the fault-tolerant tool harness.
//
// Part 1 wrote the loop by hand and left the failure policy as an exercise. This
// part keeps the loop — an ADK llmagent runs it now — and wraps the one tool that
// changes state in a harness that decides what a failure means.
//
// Run it against a real model:
//
//	go run . -faults 503,timeout
//
// and against no provider at all:
//
//	go run . -offline -faults 503,timeout
//
// The second form is the one worth watching twice. The offline model is not a
// stub: it reads the last open_refund_case observation out of its own conversation
// and decides what to do next, the way a model does. So the whole decision table
// runs — a rejected argument corrected by the model, two acquirer failures retried
// inside the harness and never mentioned to the model, and the case that gets
// opened anyway.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/dimetron/ai-eng-course/labs/internal/adkrun"
	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/internal/usage"
)

// instruction states the harness's contract to the model.
//
// It restates what the tool description already says, and that repetition is
// deliberate: the description is attached to the tool, where a model may read it
// once and then work from memory, while the instruction is re-read every turn. On
// a tool whose failure modes are the subject, redundancy is cheap.
const instruction = `You open refund cases for LEDGERWORKS merchants.

Call open_refund_case with the transaction id and the merchant id exactly as they
appear in the user's request. Never invent an id and never pass a placeholder.

Read the status of the reply:
  pending          — the case is open. Tell the user the case id.
  invalid_argument — the observation names the field that was rejected. Correct that
                     field from the user's request and call once more. If the request
                     does not contain it, ask the user for it instead of guessing.
  unavailable      — the acquirer is down. Do not call again; tell the user to try later.
  stopped          — you have run out of attempts to fix the arguments. Stop.

Answer in two or three sentences.`

func main() { os.Exit(runCLI(os.Args[1:], os.Stdout)) }

// runCLI is the whole program. It takes its output as an argument rather than
// reaching for os.Stdout so a test can read the report — the metrics line is part
// of what this example teaches, so it is part of what the tests assert.
//
// Exit codes: 0 the run completed, 1 the run failed or hit a fatal tool error,
// 2 the command line was wrong. The distinction matters here more than usual: a
// fatal tool error does NOT stop an ADK run (see the note at the end), so the exit
// code is the only place that failure surfaces to a shell.
func runCLI(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("week4-part2", flag.ContinueOnError)
	task := fs.String("task", defaultTask, "the refund request to act on")
	faults := fs.String("faults", "", "failures to inject, in order: timeout, 503, 429, corrupt")
	offline := fs.Bool("offline", false, "use the built-in stand-in model instead of calling a provider")
	priceIn := fs.Float64("price-in", 0, "USD per million input tokens, if you want the run costed")
	priceOut := fs.Float64("price-out", 0, "USD per million output tokens")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	queued, err := parseFaults(*faults)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	plan := &FaultPlan{}
	plan.Push(queued...)

	// The world outlives the agent on purpose. The case that got opened is the
	// only durable effect of this run, and it is not in the text the agent
	// returned — a model can apologise convincingly about a refund it did open,
	// and about one it did not.
	cases := &MemCases{}
	h := &Harness{
		Desk: &Desk{Cases: cases, Faults: plan},

		// The harness's own log goes to stderr: on stdout it would interleave with
		// the report the learner is reading, and a structured failure log is for
		// whoever is operating the thing, not for whoever is watching a demo.
		Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),

		// A real pause between retries, short enough to sit through. The transient
		// path is invisible by design — the model never learns the acquirer failed —
		// so the pause is the only evidence a human gets that a retry happened.
		Backoff: 250 * time.Millisecond,
	}

	llm, label := resolveModel(*offline)
	meter := &usage.Meter{}

	openTool, err := functiontool.New(functiontool.Config{
		Name: ToolName,
		// The description carries the failure vocabulary, because a model cannot
		// apply a policy nobody stated. See harness.go.
		Description: toolDescription,
	}, h.OpenRefundCase)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build tool: %v\n", err)
		return 1
	}

	refundAgent, err := llmagent.New(llmagent.Config{
		Name:        "refund_agent",
		Description: "Opens refund cases with the acquirer for LEDGERWORKS merchants.",
		// The decorator, not the model, so that every turn this agent runs is
		// counted. From here on the framework makes the call, and nothing hands the
		// response back to us.
		Model:       usage.Metering{Inner: llm, Meter: meter},
		Instruction: instruction,
		Tools:       []tool.Tool{openTool},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "build agent: %v\n", err)
		return 1
	}

	var price *usage.Price
	if *priceIn > 0 || *priceOut > 0 {
		price = &usage.Price{InPerM: *priceIn, OutPerM: *priceOut}
	}

	fmt.Fprintf(out, "week 4, part 2 — the fault-tolerant tool harness\n")
	fmt.Fprintf(out, "model:  %s\n", label)
	fmt.Fprintf(out, "policy: %s\n", h.Policy())
	fmt.Fprintf(out, "task:   %s\n\n", *task)

	answer, runErr := adkrun.Run(context.Background(), refundAgent, *task, out)

	fmt.Fprintln(out)
	if answer != "" {
		fmt.Fprintf(out, "answer: %s\n", answer)
	}

	// The report is written even when the run failed. A crash that also loses the
	// harness counters is the failure mode that hides a harness retrying forever.
	n := cases.Count()
	switch {
	case n > 0:
		fmt.Fprintf(out, "refund cases opened: %d\n", n)
	case h.Halted() != nil:
		fmt.Fprintln(out, "refund cases opened: 0 — the run was halted before the acquirer accepted a call")
	default:
		fmt.Fprintln(out, "refund cases opened: 0 — the acquirer never accepted a call")
	}
	fmt.Fprintln(out, h.Line())

	total := meter.Total()
	if *offline {
		// Not the usual Line: the stand-in reports no usage, so printing
		// "0 in + 0 out" beside a price would be a fabricated bill. A zero that means
		// "nobody measured" has to say so, or it reads as a free run.
		fmt.Fprintf(out, "tokens: %d model calls, no usage reported — the offline stand-in does not invent token counts\n", total.Calls)
	} else {
		fmt.Fprintln(out, total.Line(price))
	}

	// A run that ends with faults still queued did not exercise what the operator
	// asked for. Printed rather than assumed, because "the retry never happened"
	// and "the retry happened and I misread the log" look identical otherwise.
	fmt.Fprintf(out, "faults left in the plan: %d\n", plan.Len())

	// The exit code is the caller's job, and this is the part of the example that
	// is easiest to get wrong. ADK turns a tool handler's error into an observation
	// the model reads — it does not abort the run, and a panic is recovered into an
	// error before that. So a fatal failure arrives here as a *successful* run whose
	// answer reads like a normal report. The harness recorded it instead, and this is
	// where the record becomes a non-zero exit.
	code := 0
	if halted := h.Halted(); halted != nil {
		fmt.Fprintf(out, "halted: %v\n", halted)
		fmt.Fprintf(os.Stderr, "halted: %v\n", halted)
		code = 1
	}
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", runErr)
		code = 1
	}
	return code
}

// resolveModel picks the model the agent talks to. Same shape as part 1: a real
// provider by default, and a stand-in that says so when there is none, so a
// learner with no key still watches the whole decision table run.
func resolveModel(offline bool) (model.LLM, string) {
	if offline {
		return newDemoModel(), "offline stand-in — no provider was called"
	}
	if err := modelcfg.LoadEnv("."); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	llm, choice, err := modelcfg.Load(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "no model provider: %v\n", err)
		fmt.Fprintln(os.Stderr, "falling back to the offline stand-in — pass -offline to skip this probe")
		return newDemoModel(), "offline stand-in — no provider was called"
	}
	return llm, choice.Reason
}
