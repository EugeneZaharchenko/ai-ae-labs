package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func request(t *testing.T, client *http.Client, method, url, body string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(data)
}

const sessionPath = "/api/apps/first_graph_agent/users/learner/sessions/demo"
const runBody = `{"appName":"first_graph_agent","userId":"learner","sessionId":"demo","newMessage":{"role":"user","parts":[{"text":"txn-2026-07-118845 A-114"}]}}`

func TestADKServiceWithoutKey(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	svc, err := newService()
	if err != nil {
		t.Fatal(err)
	}
	checkADKService(t, svc)
}

func TestListenAddress(t *testing.T) {
	for _, tc := range []struct{ port, want string }{
		{"", "127.0.0.1:8080"},
		{"8080", "0.0.0.0:8080"},
		{"9090", "0.0.0.0:9090"},
		{"0", ""}, {"65536", ""}, {"bad", ""},
	} {
		got, err := listenAddress(tc.port)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Fatalf("listenAddress(%q) = %q, %v", tc.port, got, err)
		}
	}
}

type observedListener struct {
	net.Listener
	closed chan struct{}
	once   sync.Once
}

func (l *observedListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(func() { close(l.closed) })
	return err
}

func TestShutdown(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "drains active request"
		if timeout {
			name = "timeout closes active request"
		}
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			handlerDone := make(chan struct{})
			svc := &service{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				select {
				case <-release:
					if _, err := io.WriteString(w, "finished"); err != nil {
						t.Error(err)
					}
				case <-r.Context().Done():
				}
			})}
			base, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			listener := &observedListener{Listener: base, closed: make(chan struct{})}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			t.Cleanup(func() { close(release) })
			done := make(chan error, 1)
			drain := 5 * time.Second
			if timeout {
				drain = 0
			}
			go func() { done <- svc.serve(ctx, listener, drain) }()
			client := &http.Client{Timeout: 10 * time.Second}
			resp, err := client.Get("http://" + listener.Addr().String() + "/api/held")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			cancel()
			select {
			case <-listener.closed:
			case <-time.After(10 * time.Second):
				t.Fatal("listener did not close")
			}
			if !svc.draining.Load() {
				t.Fatal("readiness not cleared")
			}
			if !timeout {
				select {
				case err := <-done:
					t.Fatalf("returned before active request completed: %v", err)
				default:
				}
				release <- struct{}{}
				body, err := io.ReadAll(resp.Body)
				if err != nil || string(body) != "finished" {
					t.Fatalf("active response = %q, %v", body, err)
				}
			}
			select {
			case err := <-done:
				if timeout && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("timeout error = %v", err)
				}
				if !timeout && err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("shutdown did not finish")
			}
			select {
			case <-handlerDone:
			case <-time.After(10 * time.Second):
				t.Fatal("active handler was not cancelled")
			}
		})
	}
}

func TestServeFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	svc := &service{handler: http.NotFoundHandler()}
	if err := svc.serve(t.Context(), listener, time.Second); err == nil {
		t.Fatal("closed listener must fail")
	}
}
func checkADKService(t *testing.T, svc *service) {
	t.Helper()
	server := httptest.NewServer(svc)
	defer server.Close()
	for _, path := range []string{"/health", "/readyz"} {
		status, _, body := request(t, server.Client(), "GET", server.URL+path, "")
		if status != 200 || body != "OK\n" {
			t.Fatalf("%s: %d %s", path, status, body)
		}
	}
	status, _, body := request(t, server.Client(), "POST", server.URL+"/api/run_sse", runBody)
	if status != 404 {
		t.Fatalf("missing session: %d %s", status, body)
	}
	status, _, body = request(t, server.Client(), "POST", server.URL+sessionPath, "{}")
	if status != 200 {
		t.Fatalf("create session: %d %s", status, body)
	}
	for _, want := range []string{"pending", "already_open"} {
		status, headers, body := request(t, server.Client(), "POST", server.URL+"/api/run_sse", runBody)
		if status != 200 || !strings.HasPrefix(headers.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("SSE: %d %v %s", status, headers, body)
		}
		for _, text := range []string{"data:", "refund:last_case_id", "rc-txn-2026-07-118845-A-114", want} {
			if !strings.Contains(body, text) {
				t.Fatalf("missing %q in SSE: %s", text, body)
			}
		}
	}
	status, _, body = request(t, server.Client(), "GET", server.URL+sessionPath, "")
	if status != 200 || !strings.Contains(body, "refund:last_case_id") {
		t.Fatalf("persisted session state: %d %s", status, body)
	}
	for _, tc := range []struct {
		body string
		code int
		want string
	}{
		{`{`, 400, "failed to decode"},
		{strings.ReplaceAll(runBody, "A-114", "Z-999"), 200, "event: error"},
		{strings.ReplaceAll(runBody, "first_graph_agent", "missing_agent"), 404, "failed to find"},
	} {
		status, _, body := request(t, server.Client(), "POST", server.URL+"/api/run_sse", tc.body)
		if status != tc.code || !strings.Contains(body, tc.want) {
			t.Fatalf("negative request: %d %s", status, body)
		}
	}
	svc.draining.Store(true)
	for _, path := range []string{"/readyz", "/api/run_sse"} {
		status, _, body := request(t, server.Client(), "POST", server.URL+path, runBody)
		if status != 503 {
			t.Fatalf("drain %s: %d %s", path, status, body)
		}
	}
	status, _, _ = request(t, server.Client(), "GET", server.URL+"/health", "")
	if status != 200 {
		t.Fatalf("liveness during drain = %d", status)
	}
}
