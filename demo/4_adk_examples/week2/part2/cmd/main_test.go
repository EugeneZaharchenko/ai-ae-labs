package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/internal/adkrun"
)

func TestPrepareNode(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	out, err := prepareNode(ctx, "txn-2026-07-118845,A-114")
	if err != nil {
		t.Fatalf("prepareNode failed: %v", err)
	}
	if !strings.Contains(out, "rc-txn-2026-07-118845-A-114") || !strings.Contains(out, "A-114") {
		t.Errorf("unexpected output: %s", out)
	}
}

func TestPrepareNode_Invalid(t *testing.T) {
	ctx := &agent.StrictContextMock{Ctx: context.Background()}
	_, err := prepareNode(ctx, "single-field")
	if err == nil {
		t.Fatal("expected error for invalid input")
	}
}

func TestHealthHandler(t *testing.T) {
	a, err := adkrun.Pipeline("test", "test agent",
		workflow.NewFunctionNode[string, string]("echo",
			func(ctx agent.Context, input string) (string, error) { return input, nil },
			adkrun.NodeConfig()))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewAgentService(a, ":0")
	svc.ready = true

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	svc.handleHealth(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("expected status 'ok', got %q", body["status"])
	}
}

func TestReadyHandler_NotReady(t *testing.T) {
	a, _ := adkrun.Pipeline("test", "test agent",
		workflow.NewFunctionNode[string, string]("echo",
			func(ctx agent.Context, input string) (string, error) { return input, nil },
			adkrun.NodeConfig()))
	svc := NewAgentService(a, ":0")

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	svc.handleReady(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
}

func TestChatHandler(t *testing.T) {
	a, err := adkrun.Pipeline("test", "test agent",
		workflow.NewFunctionNode[string, string]("echo",
			func(ctx agent.Context, input string) (string, error) { return "echo: " + input, nil },
			adkrun.NodeConfig()))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewAgentService(a, ":0")
	svc.ready = true

	body := `{"message":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svc.handleChat(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}
