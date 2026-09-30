package main

// The ADK web UI draws its agent graph from ADK's own generator, which reads
// Reveal(agent).Tools and ignores .Toolsets. These tests drive that exact
// handler — the real one the UI calls — so "the graph shows every tool" is an
// assertion rather than a hope. They are the regression guard for the bug
// where the UI showed 2 nodes (agent + get_exchange_rate) while the model was
// offered 6 tools.
//
// The generator lives in an ADK-internal package, so the handler is the only
// public seam. gorilla/mux is the same router ADK uses; SetURLVars supplies
// the app_name path variable the handler reads.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/server/adkrest/controllers"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolutils"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/week1/internal/mcptool"
)

// fakeToolset is a Toolset whose tools are known without a network call, so
// the graph can be asserted offline. It stands in for mcptoolset, whose tools
// only exist after a live tools/list.
type fakeToolset struct {
	name  string
	tools []tool.Tool
	err   error
}

func (f *fakeToolset) Name() string { return f.name }

func (f *fakeToolset) Tools(_ agent.ReadonlyContext) ([]tool.Tool, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.tools, nil
}

// stubTool is a minimal but realistic tool: it carries a declaration and packs
// itself, which is what toolPreprocess requires of every tool before a request
// can be assembled (real MCP tools do this in mcptoolset's mcpTool).
type stubTool struct{ name string }

func (s stubTool) Name() string        { return s.name }
func (s stubTool) Description() string { return "stub tool " + s.name }
func (s stubTool) IsLongRunning() bool { return false }

func (s stubTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: s.name, Description: s.Description()}
}

// ProcessRequest mirrors mcptoolset's mcpTool: pack this tool into the request.
// PackTool rejects a duplicate name, so this is where the Tools/Toolsets
// double-listing trap surfaces.
func (s stubTool) ProcessRequest(_ agent.Context, req *model.LLMRequest) error {
	return toolutils.PackTool(req, s)
}

// graphDot builds the agent, serves it through ADK's real graph handler and
// returns the rendered DOT source.
func graphDot(t *testing.T, m model.LLM, extraTools ...tool.Tool) string {
	t.Helper()

	a, err := NewAgent(m, fixture(), extraTools...)
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}

	ctrl := controllers.NewAgentGraphAPIController(agent.NewSingleLoader(a))
	req := httptest.NewRequest(http.MethodGet, "/dev/apps/currency_agent/build_graph_image", nil)
	req = mux.SetURLVars(req, map[string]string{"app_name": "currency_agent"})
	rec := httptest.NewRecorder()

	ctrl.BuildGraphImageHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("graph handler status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// Without a "node" parameter the UI preloads every level, keyed by agent
	// name; the root agent's name maps to the root level.
	var out map[string]controllers.DotGraph
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode graph response: %v; body = %s", err, rec.Body.String())
	}
	g, ok := out["currency_agent"]
	if !ok {
		t.Fatalf("graph response has no currency_agent entry; got keys %v", keys(out))
	}
	return g.DotSrc
}

func keys(m map[string]controllers.DotGraph) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestGraphShowsEagerlyResolvedTools is the regression test for the 2-node
// graph. A tool supplied through a resolved toolset must appear as its own
// node, because that is exactly what the eager ResolveToolsets path does.
func TestGraphShowsEagerlyResolvedTools(t *testing.T) {
	m := fakellm.New("fake", fakellm.TextTurn("ok"))

	ts := &fakeToolset{name: "mcp_tool_set", tools: []tool.Tool{
		stubTool{name: "mono_currency_rates"},
		stubTool{name: "mono_bank_sync"},
	}}

	extra, err := mcptool.ResolveToolsets(context.Background(), ts)
	if err != nil {
		t.Fatalf("mcptool.ResolveToolsets() error = %v", err)
	}

	dot := graphDot(t, m, extra...)

	// All three tools must be nodes, not just the local one. This is the
	// assertion that fails on the pre-fix wiring, where the MCP tools sat in
	// Toolsets and were never drawn.
	for _, want := range []string{
		"get_exchange_rate", "mono_currency_rates", "mono_bank_sync",
	} {
		if !strings.Contains(dot, want) {
			t.Errorf("graph is missing node %q; DOT source:\n%s", want, dot)
		}
	}
	for _, want := range []string{
		"currency_agent->get_exchange_rate",
		"currency_agent->mono_currency_rates",
		"currency_agent->mono_bank_sync",
	} {
		if !strings.Contains(dot, want) {
			t.Errorf("graph is missing edge %q; DOT source:\n%s", want, dot)
		}
	}
}

// TestGraphShowsOnlyLocalToolWithoutMCP pins the core lab shape: with no
// resolved tools the graph is the agent plus one tool. This keeps the original
// 2-node rendering honest for the no-MCP path.
func TestGraphShowsOnlyLocalToolWithoutMCP(t *testing.T) {
	m := fakellm.New("fake", fakellm.TextTurn("ok"))

	dot := graphDot(t, m)

	if !strings.Contains(dot, "get_exchange_rate") {
		t.Errorf("graph is missing the local tool; DOT source:\n%s", dot)
	}
	if strings.Contains(dot, "mono_") {
		t.Errorf("graph mentions an MCP tool with no MCP configured; DOT source:\n%s", dot)
	}
}

// TestToolsAreListedExactlyOnce guards the duplicate-tool trap. ADK appends
// toolset tools onto Tools at call time (tools_processor.go) before packing
// each one, and PackTool rejects a repeated name. So a tool must be reachable
// through Tools or through a Toolset, never both: listing it twice would make
// every request fail the run with `duplicate tool`. The eager path keeps the
// tools in Tools only, and this asserts each lands as exactly one graph node.
func TestToolsAreListedExactlyOnce(t *testing.T) {
	m := fakellm.New("fake", fakellm.TextTurn("ok"))

	dot := graphDot(t, m,
		stubTool{name: "mono_currency_rates"},
		stubTool{name: "mono_bank_sync"},
	)

	for _, name := range []string{"mono_currency_rates", "mono_bank_sync"} {
		if n := strings.Count(dot, name+" ["); n != 1 {
			t.Errorf("tool %q appears in %d node definitions, want exactly 1; DOT source:\n%s", name, n, dot)
		}
	}
}

// TestWithheldToolIsAbsentFromGraph ties the policy to the thing the user
// reported: the withheld tool must not appear in the agent graph, so what the
// UI shows stays equal to what the model is offered.
func TestWithheldToolIsAbsentFromGraph(t *testing.T) {
	m := fakellm.New("fake", fakellm.TextTurn("ok"))

	ts := &fakeToolset{name: "mcp_tool_set", tools: []tool.Tool{
		stubTool{name: "mono_currency_rates"},
		stubTool{name: "mono_set_webhook"},
	}}
	all, err := mcptool.ResolveToolsets(context.Background(), ts)
	if err != nil {
		t.Fatalf("mcptool.ResolveToolsets() error = %v", err)
	}

	dot := graphDot(t, m, mcptool.Withhold(all, mcptool.Withheld)...)

	if !strings.Contains(dot, "mono_currency_rates") {
		t.Errorf("graph is missing the offered tool mono_currency_rates; DOT source:\n%s", dot)
	}
	if strings.Contains(dot, "mono_set_webhook") {
		t.Errorf("graph shows the withheld tool mono_set_webhook; DOT source:\n%s", dot)
	}
}

// TestLoadEnvProvidesMonoToken documents the apps/.env contract the MCP server
// depends on: LoadEnv puts MONO_TOKEN in this process environment, and
// exec.Command (nil cmd.Env) passes it on to the server subprocess.
func TestLoadEnvProvidesMonoToken(t *testing.T) {
	// Skip when the key is already exported: LoadEnv never overrides an
	// existing variable, so the test would prove nothing about the file.
	if _, exported := os.LookupEnv("MONO_TOKEN"); exported {
		t.Skip("MONO_TOKEN already exported; env wins over apps/.env by design")
	}
	if err := modelcfg.LoadEnv("."); err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	v, ok := os.LookupEnv("MONO_TOKEN")
	if !ok || v == "" {
		t.Skipf("no MONO_TOKEN in apps/.env; the public tools need no token")
	}
	// The child sees the same environment exec.Command would give it.
	out, err := exec.Command("sh", "-c", `printf '%s' "${MONO_TOKEN:+present}"`).Output()
	if err != nil {
		t.Fatalf("probe subprocess: %v", err)
	}
	if string(out) != "present" {
		t.Errorf("MCP server subprocess would not inherit MONO_TOKEN; got %q", string(out))
	}
}

// TestRunPacksEveryToolWithoutDuplicates is the end-to-end guard for the
// duplicate-tool trap. It drives a real Runner with a scripted model, so ADK
// performs its actual request assembly: tools_processor appends toolset tools
// onto Tools, then toolPreprocess packs each one and PackTool rejects a
// repeated name. The tool call in the script forces that path to run.
//
// If a tool were ever listed in both Tools and Toolsets, this fails with
// `duplicate tool: "mono_currency_rates"` instead of passing.
func TestRunPacksEveryToolWithoutDuplicates(t *testing.T) {
	m := fakellm.New("fake",
		fakellm.CallTurn("mono_currency_rates", map[string]any{}),
		fakellm.TextTurn("done"),
	)

	a, err := NewAgent(m, fixture(),
		stubTool{name: "mono_currency_rates"},
		stubTool{name: "mono_bank_sync"},
	)
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}

	r, err := runner.New(runner.Config{
		AppName:           "currency_agent",
		Agent:             a,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}

	ctx := context.Background()
	msg := &genai.Content{Role: "user", Parts: []*genai.Part{{Text: "rate please"}}}
	for _, err := range r.Run(ctx, "u", "s", msg, agent.RunConfig{}) {
		if err != nil {
			// The stub tool cannot actually execute (it has no Run method),
			// so a dispatch failure is expected. What must NOT appear is a
			// duplicate-tool error from request assembly.
			if strings.Contains(err.Error(), "duplicate tool") {
				t.Fatalf("tool packing produced a duplicate: %v", err)
			}
		}
	}

	reqs := m.Requests()
	if len(reqs) == 0 {
		t.Fatal("the model was never called; request assembly did not run")
	}

	// Every offered tool must be declared exactly once in the packed request.
	packed := map[string]int{}
	for _, rq := range reqs {
		for _, gt := range rq.Config.Tools {
			for _, decl := range gt.FunctionDeclarations {
				packed[decl.Name]++
			}
		}
	}
	for _, want := range []string{"get_exchange_rate", "mono_currency_rates", "mono_bank_sync"} {
		if packed[want] == 0 {
			t.Errorf("tool %q was never declared to the model; packed = %v", want, packed)
		}
	}
	if n := packed["mono_currency_rates"]; n != len(reqs) {
		t.Errorf("mono_currency_rates declared %d times across %d requests, want once per request",
			n, len(reqs))
	}
}
