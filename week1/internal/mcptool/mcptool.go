// Package mcptool is the external MCP tool boundary, made real.
//
// The lecture's "MCP пізніше" slide said: today the tool lives in our Go code,
// MCP is the next layer. This package is that layer, wired to a real Ukrainian
// MCP server: dimetron/mono-go-mcp (github.com/dimetron/mono-go-mcp), which
// exposes the monobank open API as 5 tools through the official Go MCP SDK
// (github.com/modelcontextprotocol/go-sdk) over stdio.
//
// Two shapes are taught in this repo; this lab uses the first:
//
//  1. ADK `mcptoolset.New` — the MCP server's tools become ADK tools the
//     model calls like any local tool. This is the default production path
//     and the one wired into the agent here.
//  2. Raw SDK `mcp.NewClient` + `mcp.CommandTransport` — for agents outside
//     ADK. Shown in Homework's 🔥 bonus; compile-verified in scratch.
//
// The boundary teaching point is unchanged from the lecture: the MCP server
// validates input against its own `inputSchema` before executing, and our
// side still sanitizes the output. The protocol moves the boundary; it does
// not remove the responsibility.
//
// It lives outside the lab package because the student's job is the tool
// contract in rates.go, not the transport that carries someone else's tools.
package mcptool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"
)

// Bin resolves the mono-go-mcp server binary: MONO_MCP_BIN first, then
// $GOPATH/bin, then $HOME/go/bin. A missing binary is an explicit error, not a
// silent degradation — the agent still works with its local tools only (least
// agency: the model sees exactly the tools we wired).
func Bin() (string, error) {
	if bin := os.Getenv("MONO_MCP_BIN"); bin != "" {
		if st, err := os.Stat(bin); err == nil && !st.IsDir() {
			return bin, nil
		}
		return "", fmt.Errorf("MONO_MCP_BIN=%s does not exist", bin)
	}
	home := os.Getenv("HOME")
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		gopath = filepath.Join(home, "go")
	}
	for _, dir := range []string{filepath.Join(gopath, "bin"), filepath.Join(home, "go", "bin")} {
		if dir == "" || dir == filepath.Join("", "bin") {
			continue
		}
		candidate := filepath.Join(dir, "mono-go-mcp")
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("mono-go-mcp binary not found: install it with " +
		"`go install github.com/dimetron/mono-go-mcp/cmd/mono-go-mcp@latest` " +
		"or set MONO_MCP_BIN to its path")
}

// Toolset builds an ADK Toolset wired to the mono-go-mcp server over stdio.
// The public tools (mono_currency_rates, mono_bank_sync) need no token; the
// /personal/* tools need MONO_TOKEN in the environment — the lab's LoadEnv has
// already loaded it from apps/.env by the time this runs.
//
// Optional env: MONO_MCP_BIN (explicit binary path), MONO_TOKEN (passed
// through to the server process).
func Toolset() (tool.Toolset, error) {
	bin, err := Bin()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin)
	// The server loads its own .env and honors MONO_TOKEN from the
	// environment; nothing to pass here beyond what LoadEnv set.
	return mcptoolset.New(mcptoolset.Config{
		Transport: &mcp.CommandTransport{Command: cmd},
	})
}

// Withheld names MCP tools the agent deliberately does NOT offer the model,
// even though the server exposes them.
//
// mono_set_webhook POSTs to monobank's /personal/webhook and mutates account
// state. The lab's rule is least agency: a tool the exercise never needs is a
// tool the model cannot misuse. Keeping the list here — rather than filtering
// silently inside Toolset — keeps the raw boundary honest, so the e2e test can
// still prove the server exposes all five tools while the agent's narrower
// surface stays an explicit, reviewable decision.
var Withheld = []string{"mono_set_webhook"}

// Withhold returns tools without the named ones, preserving order.
func Withhold(tools []tool.Tool, withheld []string) []tool.Tool {
	if len(withheld) == 0 {
		return tools
	}
	drop := make(map[string]bool, len(withheld))
	for _, name := range withheld {
		drop[name] = true
	}
	kept := make([]tool.Tool, 0, len(tools))
	for _, t := range tools {
		if drop[t.Name()] {
			continue
		}
		kept = append(kept, t)
	}
	return kept
}

// AgentTools resolves the mono-go-mcp toolset into exactly the tools the agent
// offers the model: everything the server exposes, minus Withheld. It is the
// single place the lab decides its MCP surface.
func AgentTools(ctx context.Context) ([]tool.Tool, error) {
	ts, err := Toolset()
	if err != nil {
		return nil, err
	}
	all, err := ResolveToolsets(ctx, ts)
	if err != nil {
		return nil, err
	}
	return Withhold(all, Withheld), nil
}

// HasServer is a cheap check for the agent wiring: should the external MCP
// toolset be attached? true when the binary is resolvable. Errors are reported
// by Toolset itself; this only decides.
func HasServer() bool {
	_, err := Bin()
	return err == nil
}

// resolveTimeout bounds the eager tools/list call. A local stdio server
// answers in milliseconds; the bound exists so a wedged server cannot hang
// startup, which is what the learner would otherwise stare at.
const resolveTimeout = 20 * time.Second

// resolveContext is the minimum an ADK ReadonlyContext must provide for a
// toolset's Tools() method: every MCP toolset only reads the embedded
// context.Context. StrictContextMock supplies the whole interface, so an
// unexpected call panics loudly instead of returning a zero value.
type resolveContext struct {
	agent.StrictContextMock
}

// ResolveToolsets expands toolsets into a static tool list at startup.
//
// Why this exists: the ADK web UI draws its agent graph from
// llmagentinternal.Reveal(agent).Tools — and only .Tools. The generator never
// reads .Toolsets, so a tool that arrives through a toolset is invisible in
// the graph (see the Week 1 Part 2 lab README). MCPServer tools also have no
// static existence: names like mono_currency_rates come from a live
// tools/list call, so there is nothing to draw until that call happens.
//
// Resolving here makes the graph and the model agree, at a cost worth naming:
// tools/list now runs once at startup instead of per invocation, so a tool
// added to a running MCP server no longer appears until this process
// restarts. For a stdio server started by this same process that is not a
// real loss. Each returned tool keeps its own mcpTool, which holds the
// toolset's shared connection refresher, so call-time reconnection behaviour
// is unchanged.
func ResolveToolsets(ctx context.Context, toolsets ...tool.Toolset) ([]tool.Tool, error) {
	var out []tool.Tool
	for _, ts := range toolsets {
		if ts == nil {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, resolveTimeout)
		resolved, err := ts.Tools(&resolveContext{StrictContextMock: agent.NewStrictContextMock(callCtx)})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("resolve toolset %q: %w", ts.Name(), err)
		}
		out = append(out, resolved...)
	}
	return out, nil
}

// Names renders a tool list for a startup log, so the operator can see exactly
// which tools the model will be offered.
func Names(tools []tool.Tool) string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	return strings.Join(names, ", ")
}
