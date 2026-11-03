// Week 5, part 2 — collaboration: coordinator, desks, and a human approval.
//
// The LEDGERWORKS board wants a migration plan off the legacy processor:
// risks, compliance impact, merchants still on the legacy API. Three desks
// work in parallel, a coordinator merges, and nothing reaches the board until
// a human approves that exact document.
//
//	go run .                          # a real model via internal/modelcfg
//	go run . -offline                 # no provider; grounded stand-in desks
//	go run . -offline -decision reject
//	go run . -offline -fail-desk compliance_desk
//
// What to watch: the run PAUSES at approval_gate and the process prints what
// the human is asked. The answer goes back as an ordinary user message — a
// FunctionResponse named adk_request_input, keyed by the interrupt ID — on the
// same session, and only the gate re-runs. The desk counters at the end prove
// it: one model call per desk, not two.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/internal/usage"
)

const (
	AppName   = "board_plan"
	UserID    = "marko"
	SessionID = "migration-plan"
)

const defaultTask = "Prepare a migration plan off the legacy processor for the board: risks, compliance impact, merchants still on the legacy API."

func main() { os.Exit(runCLI(os.Args[1:], os.Stdout)) }

// runCLI exit codes: 0 the run completed (sent, or not sent for a stated
// reason); 1 a run error, or the gate never asked — a brief that reached
// dispatch without a question is the defect this whole part exists to prevent;
// 2 the command line was wrong.
func runCLI(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("week5-part2", flag.ContinueOnError)
	task := fs.String("task", defaultTask, "what the board asked for")
	offline := fs.Bool("offline", false, "use grounded stand-in desks instead of calling a provider")
	decision := fs.String("decision", "approve", "the human's answer at the gate: approve or reject")
	by := fs.String("by", "Iryna Datsiuk (VP Engineering)", "who answers at the gate")
	failDesk := fs.String("fail-desk", "", "inject a failure into one desk: "+strings.Join(desks, ", "))
	priceIn := fs.Float64("price-in", 0, "USD per million input tokens, if you want the run costed")
	priceOut := fs.Float64("price-out", 0, "USD per million output tokens")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *decision != "approve" && *decision != "reject" {
		fmt.Fprintf(os.Stderr, "-decision must be approve or reject, got %q\n", *decision)
		return 2
	}
	if *failDesk != "" && !contains(desks, *failDesk) {
		fmt.Fprintf(os.Stderr, "-fail-desk must be one of %s, got %q\n", strings.Join(desks, ", "), *failDesk)
		return 2
	}

	ctx := context.Background()
	llm, label := resolveModel(*offline)
	meter := &usage.Meter{}
	eng := &Engine{LLM: usage.Metering{Inner: llm, Meter: meter}, FailDesk: *failDesk, calls: map[string]int{}}
	a, err := eng.Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build engine: %v\n", err)
		return 1
	}
	r, err := runner.New(runner.Config{AppName: AppName, Agent: a, SessionService: session.InMemoryService(), AutoCreateSession: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "build runner: %v\n", err)
		return 1
	}

	fmt.Fprintln(out, "week 5, part 2 — coordinator, desks and a human approval")
	fmt.Fprintf(out, "model: %s\n", label)
	fmt.Fprintf(out, "task:  %s\n\n", *task)

	// Turn 1: run until the graph pauses.
	req, _, err := drive(ctx, r, genai.NewContentFromText(*task, genai.RoleUser))
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		return 1
	}
	if req == nil {
		fmt.Fprintln(out, "paused: no — the graph finished without asking a human")
		return 1
	}
	hash, _ := req.Payload.(map[string]any)["hash"].(string)
	fmt.Fprintf(out, "paused at approval_gate (interrupt %s)\n", req.InterruptID)
	fmt.Fprintln(out, indent(req.Message))

	// Turn 2: the human answers. In production this arrives minutes or days
	// later from a UI or a chat button; here it is the next message on the
	// same session, which is all the runtime needs to resume.
	answer, _ := json.Marshal(Decision{Approved: *decision == "approve", By: *by, Note: "decided at the gate", Hash: hash})
	reply := genai.NewContentFromParts([]*genai.Part{{FunctionResponse: &genai.FunctionResponse{
		ID:       req.InterruptID,
		Name:     workflow.WorkflowInputFunctionCallName,
		Response: map[string]any{"response": string(answer)},
	}}}, genai.RoleUser)
	fmt.Fprintf(out, "\nresumed: %s → %s\n", *by, *decision)

	again, result, err := drive(ctx, r, reply)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resume: %v\n", err)
		return 1
	}
	if again != nil {
		fmt.Fprintf(os.Stderr, "resume: the gate asked again (%s) — the reply did not reach it\n", again.InterruptID)
		return 1
	}
	fmt.Fprintf(out, "dispatch: %s\n", result)

	return report(out, eng, meter, *offline, *priceIn, *priceOut)
}

// drive runs one message and returns the pending human request, if the graph
// paused, and the last string a node produced.
func drive(ctx context.Context, r *runner.Runner, msg *genai.Content) (*session.RequestInput, string, error) {
	var req *session.RequestInput
	var last string
	for e, err := range r.Run(ctx, UserID, SessionID, msg, agent.RunConfig{}) {
		if err != nil {
			return req, last, err
		}
		if e.RequestedInput != nil {
			req = e.RequestedInput
		}
		if s, ok := e.Output.(string); ok && s != "" {
			last = s
		}
	}
	return req, last, nil
}

func report(out io.Writer, eng *Engine, meter *usage.Meter, offline bool, priceIn, priceOut float64) int {
	calls, sent, pauses, resumes := eng.Stats()
	fmt.Fprintln(out)
	var parts []string
	for _, d := range desks {
		parts = append(parts, fmt.Sprintf("%s×%d", d, calls[d]))
	}
	fmt.Fprintf(out, "desk runs: %s\n", strings.Join(parts, ", "))
	fmt.Fprintf(out, "gate: %d question(s) asked, %d answer(s) received\n", pauses, resumes)
	if len(sent) == 0 {
		fmt.Fprintln(out, "briefs sent: 0 — dispatch refused it (see the dispatch line)")
	} else {
		fmt.Fprintf(out, "briefs sent: %d (document %s)\n", len(sent), sent[0].Hash)
	}

	total := meter.Total()
	if offline {
		fmt.Fprintf(out, "tokens: %d model calls, no usage reported — the offline stand-in does not invent token counts\n", total.Calls)
	} else {
		var price *usage.Price
		if priceIn > 0 || priceOut > 0 {
			price = &usage.Price{InPerM: priceIn, OutPerM: priceOut}
		}
		fmt.Fprintln(out, total.Line(price))
	}
	return 0
}

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

func indent(s string) string {
	return "  │ " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  │ ")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
