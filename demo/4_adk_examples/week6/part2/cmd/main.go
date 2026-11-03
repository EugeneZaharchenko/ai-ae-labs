// Week 6, part 2 — a self-improving agent, and the brakes that make it safe to run.
//
// The pattern is karpathy/autoresearch: an agent edits ONE file, runs a fixed
// cheap evaluation, keeps the change only if one metric improves, logs every
// experiment, and repeats. The ML specifics are incidental; the design is the
// reusable part. Here the file is strategy.md — how a policy-research agent
// searches a LEDGERWORKS knowledge base — and the metric is the share of golden
// facts its answers contain.
//
//	go run .                  # a real model via internal/modelcfg
//	go run . -offline         # no provider: a rule-based worker, a scripted proposer
//
// Read the output for what the loop REFUSES as much as for what it keeps: five
// experiments, one win, one escape attempt stopped at the surface, one invalid
// tool stopped by the schema, one regression and one no-gain rewording discarded.
// autoresearch itself is a single-GPU research toy from its author, not
// production tooling; this demo is a bounded version of its idea, not an
// open-ended self-modifying agent, and it does not claim to be one.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/internal/usage"
)

func main() { os.Exit(runCLI(os.Args[1:], os.Stdout)) }

// runCLI exit codes: 0 the best strategy beats the baseline and nothing outside
// the surface was written; 1 no improvement, a run error, or a surface
// violation; 2 the command line was wrong.
func runCLI(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("week6-part2", flag.ContinueOnError)
	offline := fs.Bool("offline", false, "use the built-in stand-ins instead of calling a provider")
	dir := fs.String("dir", "", "working directory for strategy.md and experiments.log (default: a fresh temp directory)")
	experiments := fs.Int("experiments", 5, "hard cap on experiments")
	minGain := fs.Float64("min-gain", 0.05, "how much a candidate must beat the best score by to be kept")
	priceIn := fs.Float64("price-in", 0, "USD per million input tokens, if you want the run costed")
	priceOut := fs.Float64("price-out", 0, "USD per million output tokens")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *experiments < 1 {
		fmt.Fprintln(os.Stderr, "-experiments must be at least 1")
		return 2
	}

	ctx := context.Background()
	work := *dir
	if work == "" {
		d, err := os.MkdirTemp("", "week6-part2-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "temp dir: %v\n", err)
			return 1
		}
		work = d
	}
	surface, err := NewSurface(work, DefaultPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	worker, proposer, label := resolveModels(*offline)
	meter := &usage.Meter{}
	eng := &Engine{Worker: usage.Metering{Inner: worker, Meter: meter}, Proposer: usage.Metering{Inner: proposer, Meter: meter}}

	fmt.Fprintln(out, "week 6, part 2 — the autoresearch loop, with brakes")
	fmt.Fprintf(out, "model:    %s\n", label)
	fmt.Fprintf(out, "surface:  one file, %s\n", surface.Path())
	fmt.Fprintf(out, "budget:   %d experiments, %d model calls per question, min gain %.2f\n", *experiments, maxModelCalls, *minGain)
	fmt.Fprintf(out, "metric:   fact coverage over %d dev questions (a pure function of the answer text)\n\n", len(Dev()))

	res, err := Improve(ctx, Config{
		MaxExperiments: *experiments, MinGain: *minGain,
		Surface: surface, LogPath: work + "/experiments.log",
	}, Env{Measure: eng.Measure, Propose: eng.Propose})
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		return 1
	}

	fmt.Fprintf(out, "baseline  score %.2f  (%d of %d facts, %d documents opened)\n", res.Baseline.Score(), res.Baseline.Found, res.Baseline.Total, res.Baseline.Reads)
	for _, r := range res.Records {
		fmt.Fprintf(out, "exp %d     %s\n", r.N, r.Reason)
		fmt.Fprintf(out, "          %q\n", r.Summary)
	}

	code := 0
	fmt.Fprintln(out)
	if res.BestM.Score() > res.Baseline.Score() {
		fmt.Fprintf(out, "improved: %.2f → %.2f (%d of %d facts, %d documents opened)\n",
			res.Baseline.Score(), res.BestM.Score(), res.BestM.Found, res.BestM.Total, res.BestM.Reads)
	} else {
		// A zero names its cause.
		fmt.Fprintf(out, "improved: no — every one of %d experiments was discarded or rejected; see the reasons above\n", len(res.Records))
		code = 1
	}
	fmt.Fprintf(out, "strategy.md now reads:\n%s\n", indent(strings.TrimRight(res.Best.Render(), "\n")))

	if extra := strayFiles(work); len(extra) > 0 {
		fmt.Fprintf(out, "surface:  VIOLATION — unexpected files in the working directory: %s\n", strings.Join(extra, ", "))
		code = 1
	} else {
		fmt.Fprintln(out, "surface:  clean — the directory holds only strategy.md and experiments.log")
	}
	fmt.Fprintf(out, "audit:    %s/experiments.log (%d lines, append-only)\n", work, len(res.Records))

	total := meter.Total()
	if *offline {
		fmt.Fprintf(out, "tokens:   %d model calls, no usage reported — the offline stand-ins do not invent token counts\n", total.Calls)
	} else {
		var price *usage.Price
		if *priceIn > 0 || *priceOut > 0 {
			price = &usage.Price{InPerM: *priceIn, OutPerM: *priceOut}
		}
		fmt.Fprintln(out, total.Line(price))
	}
	return code
}

// strayFiles lists anything in the working directory besides the two files the
// loop is allowed to produce.
func strayFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{err.Error()}
	}
	var extra []string
	for _, e := range entries {
		if n := e.Name(); n != DefaultPath && n != "experiments.log" {
			extra = append(extra, n)
		}
	}
	sort.Strings(extra)
	return extra
}

func indent(s string) string { return "    " + strings.ReplaceAll(s, "\n", "\n    ") }

func resolveModels(offline bool) (worker, proposer model.LLM, label string) {
	stand := "offline — " + workerStandIn{}.Name() + " + " + (&scriptedProposer{}).Name() +
		": this run proves the brakes and the wiring, NOT that an LLM can improve a strategy"
	if offline {
		return workerStandIn{}, &scriptedProposer{}, stand
	}
	if err := modelcfg.LoadEnv("."); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	llm, choice, err := modelcfg.Load(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "no model provider: %v\n", err)
		fmt.Fprintln(os.Stderr, "falling back to the offline stand-ins — pass -offline to skip this probe")
		return workerStandIn{}, &scriptedProposer{}, stand
	}
	return llm, llm, choice.Reason
}
