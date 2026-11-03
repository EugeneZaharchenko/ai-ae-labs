// Agent for Week 1, Part 1: ModelBenchmarkAgent
// Cross-model benchmark harness that compares models by latency/cost/quality.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/internal/adkrun"
)

// BenchmarkResult holds one model's benchmark data.
type BenchmarkResult struct {
	Model   string  `json:"model"`
	Latency float64 `json:"latency_ms"`
	Cost    float64 `json:"cost_per_1m_tokens"`
	Quality float64 `json:"quality_score"`
}

// benchmarkModels is a pure function node that returns a benchmark table.
func benchmarkModels(ctx agent.Context, input string) (string, error) {
	results := []BenchmarkResult{
		{Model: "Claude Opus 4.8", Latency: 1200, Cost: 15.00, Quality: 9.5},
		{Model: "Claude Sonnet 5", Latency: 400, Cost: 3.00, Quality: 8.5},
		{Model: "Gemini 3.1 Pro", Latency: 600, Cost: 2.50, Quality: 9.0},
		{Model: "Gemini 3.5 Flash", Latency: 200, Cost: 0.50, Quality: 7.5},
		{Model: "GPT-5.5", Latency: 800, Cost: 10.00, Quality: 9.2},
		{Model: "Grok 4.3", Latency: 500, Cost: 2.00, Quality: 8.0},
		{Model: "DeepSeek V4", Latency: 700, Cost: 1.00, Quality: 7.8},
	}

	table := fmt.Sprintf("Query: %s\n\n", input)
	table += "| Model | Latency (ms) | Cost ($/1M tok) | Quality |\n"
	table += "|-------|-------------|-----------------|--------|\n"
	for _, r := range results {
		table += fmt.Sprintf("| %s | %.0f | $%.2f | %.1f/10 |\n", r.Model, r.Latency, r.Cost, r.Quality)
	}
	table += "\nDecision framework axes: task type, latency, cost, context, data residency, reasoning effort.\n"
	return table, nil
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("model-benchmark", flag.ContinueOnError)
	query := fs.String("query", "default benchmark query", "benchmark query")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	a, err := adkrun.Pipeline(
		"ModelBenchmarkAgent",
		"Cross-model benchmark harness — compares models by latency/cost/quality",
		workflow.NewFunctionNode[string, string]("benchmark", benchmarkModels, adkrun.NodeConfig()),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	out, err := adkrun.Run(context.Background(), a, *query, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Println(out)
	return 0
}
