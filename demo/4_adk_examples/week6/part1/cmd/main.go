// Week 6, part 1 — a dynamic workflow with a maker/critic loop.
//
// The LEDGERWORKS billing service rounded a refund through float64 and lost a
// cent on large amounts (bug #423). A maker model asked to write that function
// will happily write the float64 version: it passes every example in the spec.
// This demo asks what catches it, and in what order.
//
//	go run .                     # a real model via internal/modelcfg
//	go run . -offline            # no provider; a stand-in that reads its transcript
//	go run . -offline -critics=false   # the gate alone: green, accepted, wrong
//
// The loop is maker → the Go toolchain as a deterministic gate (build, vet, the
// visible tests) → a panel of critics → feedback → maker. After the loop stops,
// the accepted candidate meets the HOLDOUT tests the maker never saw. With the
// panel on, the correctness critic rejects float64 and the second attempt passes
// the holdout. With it off, the gate alone accepts the first attempt and the
// holdout fails: green on the tests that steered the loop, wrong on the case
// nobody wrote down.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/adkrun"
	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/internal/usage"
)

func main() { os.Exit(runCLI(os.Args[1:], os.Stdout)) }

// runCLI exit codes: 0 an accepted candidate that also passes the holdout;
// 1 a run error, an exhausted budget, or an accepted candidate that fails the
// holdout — the case a loop must not be allowed to report as success;
// 2 the command line was wrong.
func runCLI(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("week6-part1", flag.ContinueOnError)
	offline := fs.Bool("offline", false, "use the built-in stand-in model instead of calling a provider")
	critics := fs.Bool("critics", true, "run the critic panel; -critics=false leaves the gate as the only reviewer")
	maxAttempts := fs.Int("max-attempts", 4, "hard cap on maker attempts")
	priceIn := fs.Float64("price-in", 0, "USD per million input tokens, if you want the run costed")
	priceOut := fs.Float64("price-out", 0, "USD per million output tokens")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *maxAttempts < 1 {
		fmt.Fprintln(os.Stderr, "-max-attempts must be at least 1")
		return 2
	}

	ctx := context.Background()
	llm, label := resolveModel(*offline)
	meter := &usage.Meter{}
	eng := &Engine{
		LLM:         usage.Metering{Inner: llm, Meter: meter},
		MaxAttempts: *maxAttempts,
		Critics:     *critics,
		Timeout:     30 * time.Second,
	}
	a, err := eng.Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build engine: %v\n", err)
		return 1
	}

	fmt.Fprintln(out, "week 6, part 1 — maker, gate and critics")
	fmt.Fprintf(out, "model:   %s\n", label)
	fmt.Fprintf(out, "policy:  up to %d attempts; gate = go build, go vet, visible tests; critics %s\n\n", *maxAttempts, onOff(*critics))

	_, runErr := adkrun.Run(ctx, a, "start", nil)
	res, loopErr := eng.Outcome()

	for _, at := range res.Attempts {
		printAttempt(out, at)
	}

	code := 0
	switch {
	case runErr != nil:
		fmt.Fprintf(os.Stderr, "run: %v\n", runErr)
		code = 1
	case !res.Accepted:
		// A zero names its cause.
		fmt.Fprintf(out, "accepted: no — %v\n", loopErr)
		code = 1
	default:
		fmt.Fprintf(out, "accepted: attempt %d\n", len(res.Attempts))
		if h := Grade(ctx, res.Code, holdoutTests, eng.Timeout); h.OK {
			fmt.Fprintln(out, "holdout:  PASS — the accepted candidate also survives the tests the maker never saw")
		} else {
			fmt.Fprintf(out, "holdout:  FAIL at %s — accepted, and wrong:\n%s\n", h.Stage, indent(firstLines(h.Output, 4)))
			code = 1
		}
	}

	total := meter.Total()
	if *offline {
		fmt.Fprintf(out, "tokens:   %d model calls, no usage reported — the offline stand-in does not invent token counts\n", total.Calls)
	} else {
		var price *usage.Price
		if *priceIn > 0 || *priceOut > 0 {
			price = &usage.Price{InPerM: *priceIn, OutPerM: *priceOut}
		}
		fmt.Fprintln(out, total.Line(price))
	}
	return code
}

func printAttempt(out io.Writer, at Attempt) {
	fmt.Fprintf(out, "attempt %d\n  %s\n", at.N, at.Gate.Line())
	if !at.Gate.OK {
		fmt.Fprintf(out, "%s\n", indent(firstLines(at.Gate.Output, 4)))
	}
	if at.Gate.OK && at.Critics {
		var parts []string
		for _, r := range Roles {
			switch v, ok := at.Verdicts[r]; {
			case !ok:
				parts = append(parts, fmt.Sprintf("%s ? (%v)", r, at.Invalid[r]))
			case v.Passed:
				parts = append(parts, fmt.Sprintf("%s ✓", r))
			default:
				parts = append(parts, fmt.Sprintf("%s ✗", r))
			}
		}
		fmt.Fprintf(out, "  critics: %s\n", strings.Join(parts, "  "))
	}
	if at.Gate.OK && !at.Critics {
		fmt.Fprintln(out, "  critics: off")
	}
	for _, c := range at.Decision.Blocking {
		fmt.Fprintf(out, "  blocks:  %s\n", firstLines(c, 2))
	}
	for _, c := range at.Decision.Advisory {
		fmt.Fprintf(out, "  advice:  %s\n", c)
	}
	if at.Decision.Accept {
		fmt.Fprintln(out, "  → accepted")
	} else {
		fmt.Fprintln(out, "  → back to the maker with the feedback above")
	}
}

func onOff(b bool) string {
	if b {
		return "on (correctness blocks; style and performance advise)"
	}
	return "OFF"
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = append(lines[:n], "…")
	}
	return strings.Join(lines, "\n")
}

func indent(s string) string { return "    " + strings.ReplaceAll(s, "\n", "\n    ") }

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
