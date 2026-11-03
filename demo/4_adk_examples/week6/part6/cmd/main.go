// Week 6, part 6 — the course harness as a kagent BYO agent.
//
// kagent runs a "bring your own" agent from any container image that serves
// the A2A protocol on port 8080 (kagent API reference, SandboxAgentSpec.byo).
// This binary is that image: an ADK Go agent behind adka2a, plus the three
// harness controls the final demo breaks on purpose:
//
//  1. surface — a BeforeToolCallback allowlist. `delete_ledger` is declared
//     (the model can see it) but not allowed: the call is refused with a reason.
//
//  2. egress — `check_egress` makes a real outbound HTTP request. On a laptop it
//     succeeds; inside a kagent SandboxAgent with no allowedDomains, outbound
//     access is denied by default, and the tool reports the failure. That is
//     the isolation proof, observable from the chat.
//
//  3. honesty — every refusal and failure names its cause in the tool result.
//
//     go run ./week6/part6/cmd -offline          # serve on :8080, no model key
//     go run ./week6/part6/cmd                   # real model via internal/modelcfg
//     curl localhost:8080/.well-known/agent-card.json
//
// deploy/ holds the kagent manifests and the Dockerfile builds the image.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2acompat/a2av0"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/server/adka2a/v2"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
)

// allowedTools is the agent's mutable surface. A tool the model can see but
// that is not listed here is refused before it runs.
var allowedTools = map[string]bool{"refund_policy": true, "check_egress": true}

func main() {
	offline := flag.Bool("offline", false, "use the built-in stand-in model instead of calling a provider")
	flag.Parse()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // kagent's BYO contract
	}
	llm := resolveModel(*offline)
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatal(err)
	}
	wire := os.Getenv("A2A_WIRE")
	if wire == "" {
		wire = wireV03 // kagent v0.10.1 pins a2a-go v0.3.15 alongside v2.3.1
	}
	h, err := newHandler(llm, publicURL(port), wire)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("refund_harness: A2A %s on :%s (model %s)", wire, port, llm.Name())
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.Serve(ln))
}

// publicURL is the URL written into the agent card. Behind kagent the card is
// re-served by the controller, so a pod-local URL is fine; A2A_PUBLIC_URL
// overrides it when the agent is exposed directly.
func publicURL(port string) *url.URL {
	if s := os.Getenv("A2A_PUBLIC_URL"); s != "" {
		if u, err := url.Parse(s); err == nil {
			return u
		}
	}
	return &url.URL{Scheme: "http", Host: "localhost:" + port}
}

// A2A wire versions this harness can speak. The card is served in a union of
// both formats either way (a2av0.NewStaticAgentCardProducer), at the v1.0 path
// and at the v0.3 path, so a client of either version can discover the agent.
const (
	wireV03 = "0.3"
	wireV10 = "1.0"
)

// newHandler builds the agent and its A2A routes: the card, and JSON-RPC at /
// in the requested wire version.
func newHandler(llm model.LLM, base *url.URL, wire string) (http.Handler, error) {
	if wire != wireV03 && wire != wireV10 {
		return nil, fmt.Errorf("A2A_WIRE=%q: want %q or %q", wire, wireV03, wireV10)
	}
	version := a2a.Version
	if wire == wireV03 {
		version = a2av0.Version
	}
	a, err := newAgent(llm)
	if err != nil {
		return nil, err
	}
	card := &a2a.AgentCard{
		Name:        a.Name(),
		Description: a.Description(),
		SupportedInterfaces: []*a2a.AgentInterface{{
			URL:             base.String(),
			ProtocolBinding: a2a.TransportProtocolJSONRPC,
			ProtocolVersion: version,
		}},
		Version:            "1.0.0",
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             adka2a.BuildAgentSkills(a),
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
	}
	exec := adka2a.NewExecutor(adka2a.ExecutorConfig{RunnerConfig: runner.Config{
		AppName: a.Name(), Agent: a, SessionService: session.InMemoryService(),
	}})
	mux := http.NewServeMux()
	cardHandler := a2asrv.NewAgentCardHandler(a2av0.NewStaticAgentCardProducer(card))
	mux.Handle(a2asrv.WellKnownAgentCardPath, cardHandler) // v1.0: agent-card.json
	mux.Handle("/.well-known/agent.json", cardHandler)     // v0.3 path, as kagent's BYO docs show
	rpc := a2asrv.NewHandler(exec)
	if wire == wireV03 {
		mux.Handle("/", a2av0.NewJSONRPCHandler(rpc))
	} else {
		mux.Handle("/", a2asrv.NewJSONRPCHandler(rpc))
	}
	return mux, nil
}

type policyArgs struct {
	AmountEUR float64 `json:"amount_eur"`
}
type policyResult struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

type egressArgs struct {
	URL string `json:"url"`
}
type egressResult struct {
	Reachable bool   `json:"reachable"`
	Detail    string `json:"detail"`
}

type deleteArgs struct {
	OrderID string `json:"order_id"`
}
type deleteResult struct {
	Deleted bool `json:"deleted"`
}

func newAgent(llm model.LLM) (agent.Agent, error) {
	policy, err := functiontool.New(functiontool.Config{Name: "refund_policy", Description: "Decide whether a refund amount needs manual review."},
		func(_ agent.Context, in policyArgs) (policyResult, error) {
			if in.AmountEUR > 200 {
				return policyResult{false, "above the 200 EUR manual-review threshold"}, nil
			}
			return policyResult{true, "under the 200 EUR manual-review threshold"}, nil
		})
	if err != nil {
		return nil, err
	}
	egress, err := functiontool.New(functiontool.Config{Name: "check_egress", Description: "Try an outbound HTTPS GET and report whether the network allowed it."},
		func(ctx agent.Context, in egressArgs) (egressResult, error) {
			return probe(ctx, in.URL), nil
		})
	if err != nil {
		return nil, err
	}
	// Declared so the model can see it; never on the allowlist.
	del, err := functiontool.New(functiontool.Config{Name: "delete_ledger", Description: "Delete a ledger entry."},
		func(_ agent.Context, _ deleteArgs) (deleteResult, error) {
			return deleteResult{}, errors.New("unreachable: the surface guard refuses this tool before it runs")
		})
	if err != nil {
		return nil, err
	}
	return llmagent.New(llmagent.Config{
		Name:                "refund_harness",
		Description:         "LEDGERWORKS refund assistant: refund policy, an egress probe, and a guarded surface.",
		Model:               llm,
		Instruction:         "You help with LEDGERWORKS refunds. Use refund_policy for amounts, check_egress to test network access. Report tool results verbatim, including refusals.",
		Tools:               []tool.Tool{policy, egress, del},
		BeforeToolCallbacks: []llmagent.BeforeToolCallback{surfaceGuard},
	})
}

// surfaceGuard is the mutable-surface brake: a non-nil result skips the tool
// and becomes its response, so the refusal and its reason reach the model.
func surfaceGuard(_ agent.Context, t tool.Tool, _ map[string]any) (map[string]any, error) {
	if allowedTools[t.Name()] {
		return nil, nil
	}
	return map[string]any{"refused": true, "reason": fmt.Sprintf("tool %q is outside this agent's allowed surface", t.Name())}, nil
}

// probe makes one bounded outbound request. Failure is a result, not an error:
// "the sandbox blocked egress" is exactly what the demo needs to show.
func probe(ctx context.Context, raw string) egressResult {
	if raw == "" {
		raw = "https://example.com"
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return egressResult{false, "bad url: " + err.Error()}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return egressResult{false, "blocked or unreachable: " + err.Error()}
	}
	resp.Body.Close()
	return egressResult{true, fmt.Sprintf("HTTP %d from %s", resp.StatusCode, req.URL.Host)}
}

func resolveModel(offline bool) model.LLM {
	if offline {
		return standIn{}
	}
	if err := modelcfg.LoadEnv("."); err != nil {
		log.Printf("warning: %v", err)
	}
	llm, choice, err := modelcfg.Load(context.Background())
	if err != nil {
		log.Printf("no model provider (%v); serving the offline stand-in", err)
		return standIn{}
	}
	log.Printf("model: %s", choice)
	return llm
}
