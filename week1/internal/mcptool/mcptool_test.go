package mcptool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
)

// stubTool is a minimal tool: name is all Names needs.
type stubTool struct{ name string }

func (s stubTool) Name() string        { return s.name }
func (s stubTool) Description() string { return "stub tool " + s.name }
func (s stubTool) IsLongRunning() bool { return false }

// TestNames pins the startup log line. It is the only place an operator sees
// the model's tool surface, so a silent empty string would hide a wiring
// mistake rather than reveal it.
func TestNames(t *testing.T) {
	tests := []struct {
		name  string
		tools []tool.Tool
		want  string
	}{
		{name: "empty", tools: nil, want: ""},
		{
			name:  "single",
			tools: []tool.Tool{stubTool{name: "get_exchange_rate"}},
			want:  "get_exchange_rate",
		},
		{
			name: "several keep call order",
			tools: []tool.Tool{
				stubTool{name: "get_exchange_rate"},
				stubTool{name: "mono_currency_rates"},
				stubTool{name: "mono_bank_sync"},
			},
			want: "get_exchange_rate, mono_currency_rates, mono_bank_sync",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Names(tt.tools); got != tt.want {
				t.Errorf("Names() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBinHonoursEnvOverride covers the MONO_MCP_BIN branch: an explicit path
// wins, and a path that does not exist is an error rather than a silent
// fallback to a different binary.
func TestBinHonoursEnvOverride(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-mcp")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	t.Setenv("MONO_MCP_BIN", bin)
	got, err := Bin()
	if err != nil {
		t.Fatalf("Bin() error = %v", err)
	}
	if got != bin {
		t.Errorf("Bin() = %q, want the MONO_MCP_BIN value %q", got, bin)
	}

	t.Setenv("MONO_MCP_BIN", filepath.Join(dir, "absent"))
	if _, err := Bin(); err == nil {
		t.Error("Bin() error = nil for a nonexistent MONO_MCP_BIN, want an error")
	} else if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("Bin() error = %q, want it to say the path does not exist", err)
	}
}

// TestAgentToolsWithholdsWebhook exercises the real MCP boundary when the
// server binary is installed. The assertion is the lab's policy: the agent
// offers the server's tools minus the mutating one, no matter what the server
// exposes. Skipped when mono-go-mcp is not installed.
func TestAgentToolsWithholdsWebhook(t *testing.T) {
	if !HasServer() {
		t.Skip("mono-go-mcp not installed; the key-free path uses the local tool only")
	}

	tools, err := AgentTools(context.Background())
	if err != nil {
		t.Skipf("mono-go-mcp unavailable: %v", err)
	}

	for _, tl := range tools {
		if tl.Name() == "mono_set_webhook" {
			t.Errorf("AgentTools() offered the withheld mutating tool %q", tl.Name())
		}
	}
	// The public rate tool must survive the filter: it is the one the lab
	// actually demonstrates.
	var sawRates bool
	for _, tl := range tools {
		if tl.Name() == "mono_currency_rates" {
			sawRates = true
		}
	}
	if !sawRates {
		t.Errorf("AgentTools() dropped mono_currency_rates; got %v", Names(tools))
	}
}

// TestWithholdRemovesNamedTools pins the exclusion policy: a mutating MCP tool
// must not reach the model, while every other tool survives in order.
func TestWithholdRemovesNamedTools(t *testing.T) {
	in := []tool.Tool{
		stubTool{name: "mono_currency_rates"},
		stubTool{name: "mono_bank_sync"},
		stubTool{name: "mono_client_info"},
		stubTool{name: "mono_statement"},
		stubTool{name: "mono_set_webhook"},
	}

	got := Withhold(in, []string{"mono_set_webhook"})

	var names []string
	for _, tl := range got {
		names = append(names, tl.Name())
	}
	want := []string{
		"mono_currency_rates", "mono_bank_sync", "mono_client_info", "mono_statement",
	}
	if len(names) != len(want) {
		t.Fatalf("Withhold() = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("Withhold()[%d] = %q, want %q (order must be preserved)", i, names[i], want[i])
		}
	}
	for _, n := range names {
		if n == "mono_set_webhook" {
			t.Errorf("withheld tool %q is still offered to the model", n)
		}
	}
}

// TestWithholdNoopCases covers the two ways the filter must not surprise: an
// empty deny-list, and a name that was never there.
func TestWithholdNoopCases(t *testing.T) {
	in := []tool.Tool{stubTool{name: "a"}, stubTool{name: "b"}}

	if got := Withhold(in, nil); len(got) != 2 {
		t.Errorf("Withhold(in, nil) dropped tools: got %d, want 2", len(got))
	}
	if got := Withhold(in, []string{"absent"}); len(got) != 2 {
		t.Errorf("Withhold with an absent name dropped tools: got %d, want 2", len(got))
	}
	if got := Withhold(nil, []string{"x"}); len(got) != 0 {
		t.Errorf("Withhold(nil, ...) = %v, want empty", got)
	}
}

// TestResolveToolsetsPropagatesError proves a broken server is reported rather
// than silently yielding zero tools — the lab's "no silent degradation" rule.
func TestResolveToolsetsPropagatesError(t *testing.T) {
	ts := &fakeToolset{name: "broken", err: context.DeadlineExceeded}

	got, err := ResolveToolsets(context.Background(), ts)
	if err == nil {
		t.Fatalf("ResolveToolsets() error = nil, want a wrapped error")
	}
	if got != nil {
		t.Errorf("ResolveToolsets() tools = %v, want nil on error", got)
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error %q does not name the failing toolset", err)
	}
	if !strings.Contains(err.Error(), "resolve toolset") {
		t.Errorf("error %q is not wrapped with context", err)
	}
}

// TestResolveToolsetsSkipsNil keeps the variadic call site forgiving: passing a
// nil toolset (an unwired optional boundary) must not panic.
func TestResolveToolsetsSkipsNil(t *testing.T) {
	got, err := ResolveToolsets(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveToolsets() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ResolveToolsets() = %v, want empty", got)
	}
}

// TestToolsetResolvesRealServer proves Toolset resolves the real mono-go-mcp
// binary and lists monobank tools over stdio — the exact production path the
// agent takes when -no-mcp is not set. This is an integration test: skipped
// when the server binary is not installed.
func TestToolsetResolvesRealServer(t *testing.T) {
	ts, err := Toolset()
	if err != nil {
		t.Skipf("mono-go-mcp not available: %v", err)
	}
	tools, err := ResolveToolsets(context.Background(), ts)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	names := Names(tools)
	t.Logf("mcp tools via ADK toolset: %v", names)
	for _, want := range []string{
		"mono_currency_rates", "mono_bank_sync", "mono_client_info",
		"mono_statement", "mono_set_webhook",
	} {
		if !strings.Contains(names, want) {
			t.Errorf("tool %q missing from tools/list; got %v", want, names)
		}
	}
}

// fakeToolset is a Toolset whose tools are known without a network call, so
// the resolution path can be asserted offline.
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
