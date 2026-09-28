package kit

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/cmd/launcher/full"
	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
)

// Models hands out one model per agent: the offline Brain model by default,
// or the single configured live model when -live is set.
type Models struct {
	live model.LLM
}

// Offline returns Models that never touch the network.
func Offline() Models { return Models{} }

// Live returns Models that give every agent the same real model.
func Live(m model.LLM) Models { return Models{live: m} }

// IsLive reports whether a real provider backs the agents.
func (m Models) IsLive() bool { return m.live != nil }

// For returns the model for the named agent. In live mode brain is ignored
// and the agent's Instruction is what steers the model.
func (m Models) For(name string, brain Brain) model.LLM {
	if m.live != nil {
		return m.live
	}
	return NewModel(name, brain)
}

// Spec describes one runnable pattern.
type Spec struct {
	// ID is the catalog id, e.g. "B4".
	ID string
	// Title is the pattern name.
	Title string
	// Input is the scripted demo message.
	Input string
	// Answers feed human pauses in the scripted demo (see Run).
	Answers []any
	// Build wires the pattern.
	Build func(Models) (agent.Agent, error)
	// Demo, if set, replaces the default single-run demo (E3 runs a queue).
	Demo func(ctx context.Context, m Models, out io.Writer) error
}

// Main is the entry point of every pattern's main.go.
func Main(spec Spec) {
	if err := Execute(context.Background(), spec, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// Execute parses args and either runs the scripted demo (no positional args)
// or hands the remaining args to the ADK launcher (console, web api webui…).
func Execute(ctx context.Context, spec Spec, args []string, out io.Writer) error {
	fs := flag.NewFlagSet(spec.ID, flag.ContinueOnError)
	fs.SetOutput(out)
	live := fs.Bool("live", false, "use the model from apps/.env (MODEL / DEFAULT_MODEL_PROVIDER) instead of the offline brain")
	input := fs.String("input", spec.Input, "message for the scripted demo")
	if err := fs.Parse(args); err != nil {
		return err
	}

	models := Offline()
	if *live {
		if err := modelcfg.LoadEnv("."); err != nil {
			return err
		}
		m, choice, err := modelcfg.Load(ctx)
		if err != nil {
			return fmt.Errorf("live mode: %w", err)
		}
		fmt.Fprintf(out, "live model: %s\n", choice.Reason)
		models = Live(m)
	}

	fmt.Fprintf(out, "━━ %s · %s ━━ (%s)\n", spec.ID, spec.Title, mode(models))
	if fs.NArg() > 0 {
		a, err := spec.Build(models)
		if err != nil {
			return err
		}
		l := full.NewLauncher()
		if err := l.Execute(ctx, &launcher.Config{AgentLoader: agent.NewSingleLoader(a)}, fs.Args()); err != nil {
			return fmt.Errorf("%w\n%s", err, l.CommandLineSyntax())
		}
		return nil
	}

	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if spec.Demo != nil {
		return spec.Demo(runCtx, models, out)
	}
	a, err := spec.Build(models)
	if err != nil {
		return err
	}
	_, err = Run(runCtx, a, out, *input, spec.Answers...)
	return err
}

func mode(m Models) string {
	if m.IsLive() {
		return "live"
	}
	return "offline, no key"
}
