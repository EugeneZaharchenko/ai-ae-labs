// Command currency_agent is the Week 1 Part 2 lab: a typed function-calling
// agent over live exchange rates (NBU or monobank, both key-free).
//
// Run it:
//
//	go run . console                # NBU rates, needs a provider key, see below
//	go run . -provider monobank console  # monobank public rates, still key-free for the model
//	go run . -offline console       # fixture rates, no key and no network
//	go run . compare                # NBU vs monobank side-by-side table, no model key
//
// Provider selection is the SAME logic as Day 1, now shared through
// internal/modelcfg (`modelcfg.Load`), so the two days cannot disagree about
// which model answers. Resolution order:
//
//  1. DEFAULT_MODEL_PROVIDER names the provider; model from MODEL, else
//     <PROVIDER>_MODEL, else that provider's table default.
//  2. MODEL alone names the model, whole — its route prefix picks the
//     provider, and pimodels answers when there is no prefix.
//  3. Neither is set — the provider comes from whichever credential is
//     present, in table order: agentgateway → ollama → gemini → openai.
//
// Every refusal is loud and names the known providers; there is no silent
// fallback. Keys come from apps/.env automatically (env vars win over the file).
//
// Note the table has no Anthropic row, so an ANTHROPIC_API_KEY alone still
// leaves this lab without a provider. That is deliberate parity with Day 1, and
// the gate is the TABLE, not the framework: pi-go/pimodels does implement
// Anthropic, so leaving it out is this lab's choice rather than a limit of the
// tools. Add the row if you want it.
//
// Verified against google.golang.org/adk/v2 v2.5.0 (released 2026-09-30,
// requires Go 1.27) on 2026-10-01. Re-check before recording.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/cmd/launcher/full"
	"google.golang.org/adk/v2/tool"

	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/week1/internal/mcptool"
)

func main() {
	// compare is a plain side-by-side utility, not an agent run: it needs no
	// model key, so it must not consume the -offline/-provider flags.
	if len(flag.Args()) == 0 && len(os.Args) > 1 && os.Args[1] == "compare" {
		runCompare(os.Args[2:])
		return
	}

	offline := flag.Bool("offline", false, "use fixture rates instead of a live API")
	providerName := flag.String("provider", "nbu", "rate provider: nbu | monobank")
	// The external MCP tool boundary (mono-go-mcp over stdio, official Go
	// SDK MCP) is attached when the server binary is resolvable — an
	// explicit, visible decision, never a silent "maybe". -no-mcp opts out.
	noMCP := flag.Bool("no-mcp", false, "run with only the local rate tool (no mono-go-mcp toolset)")
	flag.Parse()

	if err := modelcfg.LoadEnv("."); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	ctx := context.Background()

	var provider Provider = &NBUProvider{}
	switch *providerName {
	case "nbu":
		provider = &NBUProvider{}
	case "monobank":
		provider = &MonoProvider{}
	default:
		log.Fatalf("unknown provider %q (want nbu or monobank)", *providerName)
	}
	if *offline {
		provider = &FixtureProvider{
			Rates: map[string]float64{"USD": 41.5, "EUR": 45.0, "PLN": 10.0},
			Date:  "2026-07-29",
		}
		fmt.Fprintln(os.Stderr, "rates: offline fixture (USD/EUR/PLN)")
	} else {
		fmt.Fprintf(os.Stderr, "rates: %s\n", provider.Name())
	}

	// Extra tools: the local rate tool is always there; the external MCP
	// tools are added only when they resolve at startup. The model sees
	// exactly the tools we wired — never a "maybe" (least agency).
	//
	// The MCP toolset is expanded eagerly rather than passed as a Toolset so
	// the web UI agent graph shows every tool. The graph draws
	// Reveal(agent).Tools and ignores .Toolsets entirely, and MCP tool names
	// only exist after a live tools/list call. See mcptool.ResolveToolsets.
	//
	// LoadEnv has already run, so MONO_TOKEN from apps/.env is in this
	// process environment and is inherited by the MCP server subprocess.
	var extraTools []tool.Tool
	if !*noMCP {
		tools, err := mcptool.AgentTools(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mcp: %v (continuing with the local tool only)\n", err)
		} else {
			extraTools = tools
			fmt.Fprintf(os.Stderr, "mcp: mono-go-mcp toolset attached (%d tools: %s)\n",
				len(tools), mcptool.Names(tools))
			if len(mcptool.Withheld) > 0 {
				fmt.Fprintf(os.Stderr, "mcp: withheld from the model (least agency): %s\n",
					strings.Join(mcptool.Withheld, ", "))
			}
		}
	}

	m, choice, err := modelcfg.Load(ctx)
	if err != nil {
		log.Fatalf("model: %v", err)
	}
	fmt.Fprintf(os.Stderr, "model: %s (%s) — %s\n", choice.Model, choice.Provider, choice.Reason)

	a, err := NewAgent(m, provider, extraTools...)
	if err != nil {
		log.Fatalf("agent: %v", err)
	}

	l := full.NewLauncher()
	cfg := &launcher.Config{AgentLoader: agent.NewSingleLoader(a)}
	if err := l.Execute(ctx, cfg, flag.Args()); err != nil {
		log.Fatalf("run failed: %v\n\n%s", err, l.CommandLineSyntax())
	}
}

// runCompare executes the side-by-side rate comparison. It uses a fresh
// FlagSet because the agent's own flags (-offline, -provider) do not apply.
func runCompare(args []string) {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "also print the comparison as JSON")
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}

	rows, nbuDate, monoDate, err := Compare(context.Background())
	if err != nil {
		log.Fatalf("compare: %v", err)
	}
	CompareTable(rows, nbuDate, monoDate, *jsonOut)
}
