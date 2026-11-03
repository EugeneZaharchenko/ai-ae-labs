// Package adkrun provides shared test helpers for ADK workflow tests.
package adkrun

import (
	"context"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

// TestContext is a minimal test double for agent.Context that supports
// the methods DynamicNode tests need: InvocationID, Path, RunID, Branch.
// Embed it in a struct and override additional methods as needed.
type TestContext struct {
	agent.StrictContextMock
	invocationID string
	path         string
	runID        string
	branch       string
}

// NewTestContext returns a TestContext backed by ctx.
func NewTestContext(ctx context.Context) *TestContext {
	return &TestContext{
		StrictContextMock: agent.StrictContextMock{Ctx: ctx},
		invocationID:      "test-invocation-1",
		path:              "test",
		runID:             "1",
	}
}

func (m *TestContext) InvocationID() string { return m.invocationID }

func (m *TestContext) Path() string { return m.path }

func (m *TestContext) RunID() string { return m.runID }

func (m *TestContext) Branch() string { return m.branch }

func (m *TestContext) Session() session.Session { return nil }

func (m *TestContext) Agent() agent.Agent { return nil }

func (m *TestContext) AgentName() string { return "test-agent" }

func (m *TestContext) AppName() string { return "test-app" }

func (m *TestContext) UserID() string { return "test-user" }

func (m *TestContext) SessionID() string { return "test-session" }

func (m *TestContext) ReadonlyState() session.ReadonlyState { return nil }

func (m *TestContext) State() session.State { return nil }

func (m *TestContext) UserContent() *genai.Content { return nil }

func (m *TestContext) RunConfig() *agent.RunConfig { return nil }

func (m *TestContext) EndInvocation() {}

func (m *TestContext) Ended() bool { return false }

func (m *TestContext) ResumedInput(interruptID string) (any, bool) { return nil, false }

func (m *TestContext) WithContext(ctx context.Context) agent.InvocationContext { return m }

func (m *TestContext) WithDelta(d *agent.CommonContextDelta) agent.Context { return m }

func (m *TestContext) WithICDelta(d *agent.InvocationContextDelta) agent.InvocationContext { return m }

func (m *TestContext) FunctionCallID() string { return "" }

func (m *TestContext) Actions() *session.EventActions { return nil }

func (m *TestContext) SearchMemory(context.Context, string) (*memory.SearchResponse, error) {
	return nil, nil
}

func (m *TestContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }

func (m *TestContext) RequestConfirmation(hint string, payload any) error { return nil }

func (m *TestContext) SubScheduler() agent.DynamicSubScheduler { return nil }

func (m *TestContext) InvocationContext() agent.InvocationContext { return m }

func (m *TestContext) SetInvocationContext(agent.InvocationContext) {}

func (m *TestContext) WithAgentContext(ctx context.Context) agent.Context { return m }

func (m *TestContext) WithAgentTimeout(timeout time.Duration) (agent.Context, context.CancelFunc) {
	return m, func() {}
}

func (m *TestContext) WithAgentCancel() (agent.Context, context.CancelFunc) {
	return m, func() {}
}

func (m *TestContext) OutputForAncestors() []string { return nil }

func (m *TestContext) Memory() agent.Memory { return nil }

func (m *TestContext) IsolationScope() string { return "" }

func (m *TestContext) Artifacts() agent.Artifacts { return nil }
