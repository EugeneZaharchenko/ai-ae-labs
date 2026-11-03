package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2acompat/a2av0"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/remoteagent/v2"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// serve starts the harness on a loopback port in the given wire version.
func serve(t *testing.T, wire string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := &url.URL{Scheme: "http", Host: ln.Addr().String()}
	h, err := newHandler(standIn{}, base, wire)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return base.String()
}

// ask sends one message over A2A and returns the final text. With wire 0.3 the
// client uses the v0.3 compat transport — the protocol a kagent v0.10.1
// controller speaks to a BYO agent.
func ask(t *testing.T, base, msg, wire string) string {
	t.Helper()
	cfg := remoteagent.A2AConfig{Name: "harness_remote", AgentCardProvider: remoteagent.NewAgentCardProvider(base)}
	if wire == wireV03 {
		cfg.ClientProvider = remoteagent.NewA2AClientProvider(a2aclient.NewFactory(
			a2aclient.WithCompatTransport(a2av0.Version, a2a.TransportProtocolJSONRPC, a2av0.NewJSONRPCTransportFactory(a2av0.JSONRPCTransportConfig{}))))
	}
	remote, err := remoteagent.NewA2A(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, err := runner.New(runner.Config{AppName: "kagent-sim", Agent: remote, SessionService: session.InMemoryService(), AutoCreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for ev, err := range r.Run(context.Background(), "u", "s", genai.NewContentFromText(msg, genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if ev != nil && ev.LLMResponse.Content != nil {
			for _, p := range ev.LLMResponse.Content.Parts {
				if p.Text != "" {
					text = p.Text
				}
			}
		}
	}
	return text
}

func TestAgentCardServedAtBothPaths(t *testing.T) {
	base := serve(t, wireV03)
	for _, p := range []string{"/.well-known/agent-card.json", "/.well-known/agent.json"} {
		resp, err := http.Get(base + p)
		if err != nil {
			t.Fatal(err)
		}
		var card struct {
			Name   string `json:"name"`
			URL    string `json:"url"` // v0.3 field, present in the union format
			Skills []any  `json:"skills"`
		}
		err = json.NewDecoder(resp.Body).Decode(&card)
		resp.Body.Close()
		if err != nil || card.Name != "refund_harness" || card.URL == "" {
			t.Fatalf("%s: card=%+v err=%v", p, card, err)
		}
	}
}

func TestRejectsUnknownWire(t *testing.T) {
	if _, err := newHandler(standIn{}, &url.URL{}, "2.0"); err == nil {
		t.Fatal("unknown A2A_WIRE must be an error, not a silent default")
	}
}

func TestPolicyOverBothWires(t *testing.T) {
	for _, w := range []string{wireV03, wireV10} {
		got := ask(t, serve(t, w), "May we refund 120 EUR on order 8841?", w)
		if !strings.Contains(got, `"allowed":true`) {
			t.Fatalf("wire %s: got %q", w, got)
		}
	}
}

func TestSurfaceGuardRefusesWithReason(t *testing.T) {
	got := ask(t, serve(t, wireV03), "Please delete the ledger entry for order 8841", wireV03)
	if !strings.Contains(got, `"refused":true`) || !strings.Contains(got, "outside this agent's allowed surface") {
		t.Fatalf("delete_ledger must be refused with a reason, got %q", got)
	}
}

func TestEgressProbeReportsBothOutcomes(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ok.Close()
	if got := ask(t, serve(t, wireV03), "check egress to "+ok.URL, wireV03); !strings.Contains(got, `"reachable":true`) {
		t.Fatalf("reachable endpoint: got %q", got)
	}
	// A closed port fails the same way a sandbox with no allowedDomains does:
	// the request never completes, and the tool says so instead of erroring.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	if got := ask(t, serve(t, wireV03), "check egress to "+deadURL, wireV03); !strings.Contains(got, `"reachable":false`) || !strings.Contains(got, "blocked or unreachable") {
		t.Fatalf("blocked endpoint: got %q", got)
	}
}
