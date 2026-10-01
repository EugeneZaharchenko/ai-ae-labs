// Command agui_web is a Go AG-UI bridge in front of a Google ADK service, plus
// a single-file chat page embedded in the binary.
//
// The browser POSTs an AG-UI RunAgentInput to /agui. The bridge forwards the
// user's message to the ADK service's /api/run_sse, translates each ADK event
// into an AG-UI event, and streams them back over SSE. The agent keeps running
// as its own service: nothing inside the ADK process changes, and the bridge
// holds no model credentials of its own.
//
// Why the AG-UI community Go SDK and not CopilotKit: CopilotKit's frontend is
// React and needs a Node runtime in front of the agent. This demo has to stay
// buildable with the Go toolchain alone (AGENTS.md §5: the repo is Go and
// Markdown), so the bridge speaks AG-UI directly and the page renders it with
// plain JavaScript. The wire format is the same, so a CopilotKit frontend could
// be pointed at /agui later without touching this file.
//
// Verified against github.com/ag-ui-protocol/ag-ui/sdks/community/go
// v0.0.0-20261001154531-3cf4290db2fe (untagged; see README) and
// google.golang.org/adk/v2 v2.5.0, станом на 10/2026.
package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	aguissse "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
)

//go:embed web/index.html
var indexHTML []byte

const (
	// defaultAgentURL is where `task week2:day4:serve` puts the ADK service.
	defaultAgentURL = "http://127.0.0.1:8080"
	// appName must match the ADK agent's own name, not the container's. This is
	// the single most common cause of a 404 from run_sse.
	appName = "first_graph_agent"
	// userID is fixed: this demo has no login, so every browser shares one ADK
	// user. The thread id from AG-UI becomes the ADK session id instead, which
	// is what actually separates conversations.
	userID = "learner"
	// runTimeout bounds one turn. The ADK graph is local and finishes in
	// milliseconds; the ceiling exists so a wedged agent cannot hold a
	// connection open forever.
	runTimeout = 2 * time.Minute
	// maxBody caps the request body. The page sends one short message.
	maxBody = 1 << 20
)

func main() {
	agentURL := firstNonEmpty(os.Getenv("AGENT_URL"), defaultAgentURL)
	// BRIDGE_PORT, not PORT: the ADK service reads PORT, and the two binaries
	// share one container, so a single variable would put them on the same port.
	// This is why the names differ rather than both saying PORT.
	addr, err := listenAddress(os.Getenv("BRIDGE_PORT"))
	if err != nil {
		log.Fatalf("agui_web: %v", err)
	}

	// SIGTERM and Ctrl-C both shut down cleanly: the listener closes, and the
	// child agent is stopped before this process exits. Without this, `docker
	// stop` kills the bridge outright (exit 143) and the agent is orphaned
	// mid-flight — which is exactly what the first version of this file did.
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	var stopAgent func()
	if bin := strings.TrimSpace(os.Getenv("AGENT_BIN")); bin != "" {
		var err error
		stopAgent, err = startAgent(bin, ":8080")
		if err != nil {
			log.Fatalf("agui_web: start agent: %v", err)
		}
		log.Printf("agent:  started %s (pid %d)", bin, agentPID)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", index)
	mux.HandleFunc("/agui", aguiHandler(agentURL))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, "OK")
	})

	log.Printf("agui_web listening on %s", addr)
	log.Printf("  agent:  %s (app %q, user %q)", agentURL, appName, userID)
	log.Printf("  page:   http://%s/", addr)
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "127.0.0.1" {
		log.Printf("  note:   bound to %s, so this is reachable from outside the container on published port %s", host, os.Getenv("BRIDGE_PORT"))
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// No WriteTimeout: it would cut an in-flight SSE stream short.
	}

	// Shut down in a goroutine so the main path can keep serving; the error is
	// carried back on a channel rather than logged here, because this is the
	// only place that knows whether the exit was a request or a failure.
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("agui_web: %v", err)
		}
	case <-ctx.Done():
		stopSignals() // a second SIGTERM now kills immediately, as expected
		log.Printf("shutting down: no new requests, waiting for active ones")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), agentStopGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}

	if stopAgent != nil {
		stopAgent()
	}
	log.Printf("stopped")
}

// agentStopGrace bounds both the HTTP drain and the wait for the child agent.
// One constant, because they are the same deadline in practice: the agent is
// only busy while a request that the drain is waiting for is still running.
const agentStopGrace = 10 * time.Second

// startAgent launches the ADK service as a child process and returns a stop
// function. It exists so one container can hold both binaries without a shell.
//
// PORT is set explicitly rather than inherited: the child must bind every
// interface to be reachable from the sibling process and from a published port,
// and setting it here keeps that decision in one place instead of depending on
// whatever the environment happened to carry.
func startAgent(bin, addr string) (func(), error) {
	port := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		port = addr[i+1:]
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "PORT="+port)
	cmd.Stdout = os.Stderr // the agent's log joins this process's log
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	agentPID = cmd.Process.Pid

	return func() {
		if cmd.Process == nil {
			return
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(agentStopGrace):
			// The agent drains on SIGTERM; if it is still up after the grace
			// period it is wedged, and a container that never exits is worse
			// than one that leaves the child to the kernel.
			_ = cmd.Process.Kill()
			<-done
		}
	}, nil
}

// agentPID is reported in the startup log so a reader can see the child exists.
var agentPID int

// listenAddress mirrors the labs4 rule, for the same reason: an empty PORT is
// loopback-only (safe default on a laptop), and a set PORT binds every
// interface, which is what a container needs to be reachable through -p.
func listenAddress(port string) (string, error) {
	if strings.TrimSpace(port) == "" {
		return "127.0.0.1:8081", nil
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", fmt.Errorf("PORT must be an integer from 1 to 65535, got %q", port)
	}
	return net.JoinHostPort("0.0.0.0", strconv.Itoa(number)), nil
}

func index(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(indexHTML)
}

// aguiHandler is the whole bridge: one AG-UI request in, one ADK run out, and
// the ADK event stream translated back into AG-UI on the way.
func aguiHandler(agentURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var in types.RunAgentInput
		if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		text := lastUserText(in)
		if text == "" {
			http.Error(w, "no user message in the AG-UI payload", http.StatusBadRequest)
			return
		}

		// A browser cannot read the status code of a stream it already started
		// rendering, so the body must be correct before the first byte goes out:
		// the ADK session is created up front rather than mid-stream.
		ctx, cancel := context.WithTimeout(r.Context(), runTimeout)
		defer cancel()
		threadID := firstNonEmpty(in.ThreadID, "thread-1")
		if err := ensureSession(ctx, agentURL, threadID); err != nil {
			http.Error(w, fmt.Sprintf("ADK session: %v", err), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		sw := aguissse.NewSSEWriter()
		emit := func(e events.Event) error { return sw.WriteEvent(ctx, w, e) }
		runID := firstNonEmpty(in.RunID, "run-1")

		if err := emit(events.NewRunStartedEvent(threadID, runID)); err != nil {
			log.Printf("emit RUN_STARTED: %v", err)
			return
		}

		// RunErrorEvent rather than a silent close: once the stream has started
		// the HTTP status is already sent, so an error has to travel as an event
		// or the page sees an empty answer and no reason for it.
		emitErr := func(err error) {
			log.Printf("run %s: %v", runID, err)
			if e := emit(events.NewRunErrorEvent(err.Error())); e != nil {
				log.Printf("emit RUN_ERROR: %v", e)
			}
			_ = emit(events.NewRunFinishedEvent(threadID, runID))
		}

		t := translator{emit: emit}
		if err := streamAgent(ctx, agentURL, threadID, text, t.translate); err != nil {
			emitErr(err)
			return
		}
		if err := t.finish(); err != nil {
			emitErr(err)
			return
		}
		if err := emit(events.NewRunFinishedEvent(threadID, runID)); err != nil {
			log.Printf("emit RUN_FINISHED: %v", err)
		}
	}
}

// translator turns the agent's node outputs into AG-UI events.
//
// The mapping is the interesting part of this demo:
//
//   - a node whose output is a plain string is the graph's final, human-readable
//     answer, so it becomes TEXT_MESSAGE_CONTENT — the chat bubble;
//   - a node whose output is a JSON object is structured data (a parsed request
//     or an opened case), so it becomes a TOOL_CALL with its arguments. That is
//     exactly the hook a generative-UI frontend renders as a card, so this is
//     where a CopilotKit `useComponent` would receive its props.
//
// Every tool call is completed before the next event: an AG-UI stream with an
// unterminated tool call is invalid, and a result that trails the final message
// reads as though the tool were still running.
type translator struct {
	emit    func(events.Event) error
	textID  string
	toolSeq int
}

func (t *translator) translate(ev agentEvent) error {
	out := outputValue(ev.Output)
	if out == "" {
		return nil
	}
	if isJSON(out) {
		return t.toolCall(nodeName(ev.NodeInfo.Path), out)
	}
	return t.text(out)
}

func (t *translator) text(s string) error {
	if t.textID == "" {
		t.textID = "msg-1"
		if err := t.emit(events.NewTextMessageStartEvent(t.textID, events.WithRole("assistant"))); err != nil {
			return err
		}
	}
	return t.emit(events.NewTextMessageContentEvent(t.textID, s))
}

func (t *translator) toolCall(name, args string) error {
	t.toolSeq++
	id := fmt.Sprintf("tool-%d", t.toolSeq)
	if err := t.emit(events.NewToolCallStartEvent(id, name)); err != nil {
		return err
	}
	if err := t.emit(events.NewToolCallArgsEvent(id, args)); err != nil {
		return err
	}
	if err := t.emit(events.NewToolCallEndEvent(id)); err != nil {
		return err
	}
	// The result carries the arguments the node already computed: the work is
	// done by the time the event arrives, and an empty result would render as a
	// tool call that produced nothing.
	return t.emit(events.NewToolCallResultEvent("result-"+id, id, args))
}

func (t *translator) finish() error {
	if t.textID != "" {
		return t.emit(events.NewTextMessageEndEvent(t.textID))
	}
	return nil
}

// ensureSession creates the ADK session for a thread, treating "already exists"
// as success so a second message on the same thread does not fail.
func ensureSession(ctx context.Context, base, session string) error {
	endpoint := fmt.Sprintf("%s/api/apps/%s/users/%s/sessions/%s",
		strings.TrimRight(base, "/"),
		url.PathEscape(appName), url.PathEscape(userID), url.PathEscape(session))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 == 2 || strings.Contains(string(body), "already exists") {
		return nil
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

// agentEvent is the subset of an ADK event this bridge reads. Everything else
// in the payload (timestamps, invocation ids, usage) is deliberately ignored:
// the AG-UI stream carries its own identifiers.
type agentEvent struct {
	Output   json.RawMessage `json:"output"`
	NodeInfo struct {
		Path string `json:"path"`
	} `json:"nodeInfo"`
}

// streamAgent posts the message to the ADK service and calls fn once per SSE
// event. The ADK stream is newline-delimited `data:` frames, which is why this
// is a scanner rather than an EventSource client.
func streamAgent(ctx context.Context, base, session, text string, fn func(agentEvent) error) error {
	payload := map[string]any{
		"appName":   appName,
		"userId":    userID,
		"sessionId": session,
		"newMessage": map[string]any{
			"role":  "user",
			"parts": []map[string]any{{"text": text}},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode run_sse payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(base, "/")+"/api/run_sse", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build run_sse request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("call %s/api/run_sse: %w", base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("ADK run_sse returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return parseSSE(resp.Body, fn)
}

// parseSSE reads the ADK event stream and calls fn once per frame.
//
// Split out from streamAgent so the framing can be tested without an HTTP
// server: the shape being parsed (blank-line separated frames, each carrying a
// `data:` line) is a wire detail worth pinning on its own.
func parseSSE(r io.Reader, fn func(agentEvent) error) error {
	sc := bufio.NewScanner(r)
	// One node's output can exceed the 64 KiB default; a truncated event would
	// look like a graph that stopped halfway.
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)

	var data []string
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		raw := strings.Join(data, "\n")
		data = data[:0]
		var ev agentEvent
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			return nil // a frame this bridge does not model is not an error
		}
		return fn(ev)
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read ADK stream: %w", err)
	}
	return flush()
}

// nodeName turns "first_graph_agent@1/open_refund_case@1" into the tool name
// "open_refund_case", dropping the version suffixes ADK appends.
func nodeName(path string) string {
	seg := path
	if i := strings.LastIndex(seg, "/"); i >= 0 {
		seg = seg[i+1:]
	}
	if i := strings.LastIndex(seg, "@"); i > 0 {
		seg = seg[:i]
	}
	if seg == "" {
		return "agent_node"
	}
	return seg
}

// outputValue renders one node's output as text.
//
// The distinction that matters: `output` arrives as raw JSON, so a JSON *string*
// ("Кейс …") must be unquoted before it goes into a chat bubble, while a JSON
// *object* ({"case_id":…}) is structured payload. Reading the raw bytes directly
// would show the answer wrapped in escaped quotes, which is what the first
// version of this bridge did.
func outputValue(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(string(raw))
}

// isJSON reports whether a node's output is a structured object or array, as
// opposed to a plain answer. Quoted strings are deliberately excluded: they are
// chat text, not data.
func isJSON(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" || (!strings.HasPrefix(t, "{") && !strings.HasPrefix(t, "[")) {
		return false
	}
	return json.Valid([]byte(t))
}

func lastUserText(in types.RunAgentInput) string {
	for i := len(in.Messages) - 1; i >= 0; i-- {
		if in.Messages[i].Role == types.RoleUser {
			return strings.TrimSpace(fmt.Sprint(in.Messages[i].Content))
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
