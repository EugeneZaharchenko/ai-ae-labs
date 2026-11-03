// Week 6, part 4 — A2A: one agent calls another across a process boundary.
//
// A "policy" agent is published over the A2A protocol (agent card at
// /.well-known/agent-card.json, JSON-RPC at /invoke). A "support" agent in a
// different runner reaches it through remoteagent.NewA2A, the same way it would
// reach an agent owned by another team. Here both live in one process on a real
// loopback socket, so the demo runs offline — the wire format is the real one.
//
//	go run .                 # offline: scripted models, loopback socket
//	go run . -deny-callback  # BeforeRequest callback blocks the call (policy gate)
//
// What this teaches, in order of how often it bites in production:
//  1. the agent card is the contract — name, skills, interface URL, version;
//  2. the client must not trust the remote's answer as an instruction: it
//     arrives as data, and a BeforeRequest callback is the place to refuse;
//  3. A2A carries no identity by itself. Loopback and no auth is a demo, not a
//     deployment (SPIFFE inside the cluster, AAuth across org boundaries).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/remoteagent/v2"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/server/adka2a/v2"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/scripted"
)

func main() { os.Exit(runCLI(os.Args[1:], os.Stdout)) }

// runCLI exit codes: 0 answer received; 1 run error or call refused; 2 bad flags.
func runCLI(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("week6-part4", flag.ContinueOnError)
	deny := fs.Bool("deny-callback", false, "refuse the remote call in a BeforeRequest callback")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()

	base, stop, err := servePolicyAgent()
	if err != nil {
		fmt.Fprintln(out, "server:", err)
		return 1
	}
	defer stop()

	answer, err := askRemote(ctx, base, "May we refund 120 EUR on order 8841?", *deny)
	if err != nil {
		fmt.Fprintln(out, "call refused or failed:", err)
		return 1
	}
	fmt.Fprintln(out, "remote policy agent says:", answer)
	return 0
}

// servePolicyAgent publishes a scripted policy agent over A2A on 127.0.0.1.
func servePolicyAgent() (baseURL string, stop func(), err error) {
	policy, err := llmagent.New(llmagent.Config{
		Name:        "refund_policy",
		Description: "Answers whether a refund is allowed under the refund policy.",
		Model:       scripted.New("policy", scripted.Say("ALLOWED: amount is under the 200 EUR manual-review threshold.", 30)),
		Instruction: "You answer refund-policy questions.",
	})
	if err != nil {
		return "", nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	base := &url.URL{Scheme: "http", Host: ln.Addr().String()}
	const path = "/invoke"
	card := &a2a.AgentCard{
		Name:        policy.Name(),
		Description: policy.Description(),
		SupportedInterfaces: []*a2a.AgentInterface{{
			URL:             base.JoinPath(path).String(),
			ProtocolBinding: a2a.TransportProtocolJSONRPC,
			ProtocolVersion: a2a.Version,
		}},
		Version:            "1.0.0",
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             adka2a.BuildAgentSkills(policy),
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
	}
	exec := adka2a.NewExecutor(adka2a.ExecutorConfig{RunnerConfig: runner.Config{
		AppName: policy.Name(), Agent: policy, SessionService: session.InMemoryService(),
	}})
	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle(path, a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(exec)))
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	return base.String(), func() { srv.Close() }, nil
}

// askRemote wraps the remote agent in a client-side runner and returns its text.
func askRemote(ctx context.Context, baseURL, question string, deny bool) (string, error) {
	cfg := remoteagent.A2AConfig{
		Name:              "refund_policy_remote",
		AgentCardProvider: remoteagent.NewAgentCardProvider(baseURL),
	}
	if deny {
		cfg.BeforeRequestCallbacks = []remoteagent.BeforeA2ARequestCallback{
			func(agent.Context, *a2a.SendMessageRequest) (*session.Event, error) {
				return nil, fmt.Errorf("egress policy: remote agent %q is not on the allowlist", baseURL)
			},
		}
	}
	remote, err := remoteagent.NewA2A(cfg)
	if err != nil {
		return "", err
	}
	r, err := runner.New(runner.Config{AppName: "support", Agent: remote, SessionService: session.InMemoryService(), AutoCreateSession: true})
	if err != nil {
		return "", err
	}
	var text string
	for ev, runErr := range r.Run(ctx, "u", "s", genai.NewContentFromText(question, genai.RoleUser), agent.RunConfig{}) {
		if runErr != nil {
			return "", runErr
		}
		if ev != nil && ev.LLMResponse.Content != nil {
			for _, p := range ev.LLMResponse.Content.Parts {
				if p.Text != "" {
					text = p.Text
				}
			}
		}
	}
	if text == "" {
		return "", fmt.Errorf("remote agent returned no text")
	}
	return text, nil
}
