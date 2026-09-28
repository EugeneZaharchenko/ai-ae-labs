package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/server/adkrest"
	"google.golang.org/adk/v2/session"

	"github.com/dimetron/ai-eng-course/labs/week2/internal/refund"
)

type service struct {
	handler  http.Handler
	draining atomic.Bool
}

func listenAddress(port string) (string, error) {
	if port == "" {
		return "127.0.0.1:8080", nil
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", fmt.Errorf("PORT must be an integer from 1 to 65535, got %q", port)
	}
	return net.JoinHostPort("0.0.0.0", strconv.Itoa(number)), nil
}

func newService() (*service, error) {
	a, err := newGraph(&refund.Registry{})
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}
	rest, err := adkrest.NewServer(adkrest.ServerConfig{
		AgentLoader:     agent.NewSingleLoader(a),
		SessionService:  session.InMemoryService(),
		SSEWriteTimeout: 120 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("create ADK REST server: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", rest))
	mux.HandleFunc("/health", health)
	mux.HandleFunc("/readyz", health)
	return &service{handler: mux}, nil
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := fmt.Fprintln(w, "OK"); err != nil {
		log.Printf("write health response: %v", err)
	}
}

func (s *service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() && r.URL.Path != "/health" {
		http.Error(w, "service is draining", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	s.handler.ServeHTTP(w, r)
}

func (s *service) serve(ctx context.Context, listener net.Listener, drainTimeout time.Duration) error {
	server := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       60 * time.Second,
		// No global WriteTimeout: adkrest owns the SSE write deadline.
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
	}
	s.draining.Store(true)
	log.Print("draining: rejecting new work and waiting for active requests")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	err := server.Shutdown(shutdownCtx)
	if err != nil {
		// Shutdown alone does not close active connections after its deadline.
		err = errors.Join(fmt.Errorf("drain HTTP: %w", err), server.Close())
	}
	if serveErr := <-done; !errors.Is(serveErr, http.ErrServerClosed) {
		err = errors.Join(err, fmt.Errorf("serve HTTP: %w", serveErr))
	}
	return err
}
