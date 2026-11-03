// Agent for Week 2, Part 2: AgentService
// HTTP-wrapped agent with health checks, streaming, graceful shutdown.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/internal/adkrun"
)

// AgentService wraps an ADK agent as an HTTP service.
type AgentService struct {
	agent  agent.Agent
	server *http.Server
	mu     sync.Mutex
	ready  bool
}

// NewAgentService creates a new agent service.
func NewAgentService(a agent.Agent, addr string) *AgentService {
	s := &AgentService{agent: a}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/ready", s.handleReady)
	mux.HandleFunc("/chat", s.handleChat)

	s.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 60 * time.Second,
	}
	return s
}

func (s *AgentService) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *AgentService) handleReady(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ready := s.ready
	s.mu.Unlock()
	if !ready {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"status": "not ready"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
}

func (s *AgentService) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	out, err := adkrun.Run(r.Context(), s.agent, req.Message, nil)
	if err != nil {
		slog.Error("agent run failed", "error", err)
		fmt.Fprintf(w, "error: %v\n", err)
		flusher.Flush()
		return
	}
	fmt.Fprint(w, out)
	flusher.Flush()
}

// prepareNode extracts input fields.
func prepareNode(ctx agent.Context, input string) (string, error) {
	parts := strings.SplitN(input, ",", 2)
	if len(parts) < 2 {
		return "", fmt.Errorf("expected format: transaction_id,merchant_id")
	}
	txn := strings.ToLower(strings.TrimSpace(parts[0]))
	merchant := strings.ToUpper(strings.TrimSpace(parts[1]))
	return fmt.Sprintf("Opening refund case rc-%s-%s for merchant %s", txn, merchant, merchant), nil
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("agent-service", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "listen address")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	a, err := adkrun.Pipeline(
		"AgentService",
		"HTTP-wrapped ADK agent with health checks and streaming",
		workflow.NewFunctionNode[string, string]("prepare", prepareNode, adkrun.NodeConfig()),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	svc := NewAgentService(a, *addr)
	svc.mu.Lock()
	svc.ready = true
	svc.mu.Unlock()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		slog.Info("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		svc.server.Shutdown(shutdownCtx)
	}()

	slog.Info("agent service starting", "addr", *addr)
	if err := svc.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		return 1
	}
	return 0
}
