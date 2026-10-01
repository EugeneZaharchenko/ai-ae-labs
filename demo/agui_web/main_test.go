package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
)

// collect records every event a translator emits, so a test can assert on the
// exact AG-UI sequence rather than on a rendered string.
func collect(t *testing.T) (*translator, *[]events.Event) {
	t.Helper()
	var got []events.Event
	tr := &translator{emit: func(e events.Event) error {
		got = append(got, e)
		return nil
	}}
	return tr, &got
}

func typesOf(evs []events.Event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, string(e.GetBaseEvent().EventType))
	}
	return out
}

// TestTranslateMapsNodeOutput pins the whole point of the bridge: structured
// node output becomes a tool call, plain output becomes chat text. If this
// mapping drifts, the page silently stops drawing cards.
func TestTranslateMapsNodeOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		node   string
		want   []string
	}{
		{
			name:   "structured output is a tool call",
			output: `{"case_id":"rc-1","status":"pending"}`,
			node:   "first_graph_agent@1/open_refund_case@1",
			want: []string{
				"TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT",
			},
		},
		{
			name:   "plain answer is chat text",
			output: `"Кейс rc-1: статус pending"`,
			node:   "first_graph_agent@1/format@1",
			want:   []string{"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT"},
		},
		{
			name:   "empty output emits nothing",
			output: `null`,
			node:   "first_graph_agent@1/prepare@1",
			want:   nil,
		},
		{
			name:   "json object output is a tool call, not text",
			output: `{"transaction_id":"txn-1","merchant_id":"A-114"}`,
			node:   "prepare",
			want: []string{
				"TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, got := collect(t)
			ev := agentEvent{Output: json.RawMessage(tt.output)}
			ev.NodeInfo.Path = tt.node
			if err := tr.translate(ev); err != nil {
				t.Fatalf("translate: %v", err)
			}
			if diff := typesOf(*got); !equal(diff, tt.want) {
				t.Errorf("event types = %v, want %v", diff, tt.want)
			}
		})
	}
}

// TestToolCallCarriesTheNodeNameAndArgs pins that the card the page draws gets
// the node name as its title and the node's JSON as its fields.
func TestToolCallCarriesTheNodeNameAndArgs(t *testing.T) {
	tr, got := collect(t)
	ev := agentEvent{Output: json.RawMessage(`{"case_id":"rc-1"}`)}
	ev.NodeInfo.Path = "first_graph_agent@1/open_refund_case@1"
	if err := tr.translate(ev); err != nil {
		t.Fatalf("translate: %v", err)
	}

	start, ok := (*got)[0].(*events.ToolCallStartEvent)
	if !ok {
		t.Fatalf("first event = %T, want *events.ToolCallStartEvent", (*got)[0])
	}
	if start.ToolCallName != "open_refund_case" {
		t.Errorf("tool name = %q, want open_refund_case (version suffix dropped)", start.ToolCallName)
	}
	args, ok := (*got)[1].(*events.ToolCallArgsEvent)
	if !ok {
		t.Fatalf("second event = %T, want *events.ToolCallArgsEvent", (*got)[1])
	}
	if args.Delta != `{"case_id":"rc-1"}` {
		t.Errorf("args = %q, want the node's JSON verbatim", args.Delta)
	}
	// The result must reference the same call, otherwise a frontend cannot pair
	// them and renders the tool as still running.
	result, ok := (*got)[3].(*events.ToolCallResultEvent)
	if !ok {
		t.Fatalf("fourth event = %T, want *events.ToolCallResultEvent", (*got)[3])
	}
	if result.ToolCallID != start.ToolCallID {
		t.Errorf("result toolCallId = %q, want %q", result.ToolCallID, start.ToolCallID)
	}
}

// TestEachToolCallIsCompletedBeforeTheNext pins the invariant that broke the
// first version: a result that trails the following call is an invalid stream.
func TestEachToolCallIsCompletedBeforeTheNext(t *testing.T) {
	tr, got := collect(t)
	for _, node := range []string{"prepare", "open_refund_case"} {
		ev := agentEvent{Output: json.RawMessage(`{"n":1}`)}
		ev.NodeInfo.Path = node
		if err := tr.translate(ev); err != nil {
			t.Fatalf("translate %s: %v", node, err)
		}
	}
	if want := []string{
		"TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT",
		"TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT",
	}; !equal(typesOf(*got), want) {
		t.Errorf("event types = %v, want %v", typesOf(*got), want)
	}
}

// TestTextIsAccumulatedIntoOneMessage pins that several plain outputs become one
// bubble, not one bubble each: an agent that answers in pieces should read as a
// single reply.
func TestTextIsAccumulatedIntoOneMessage(t *testing.T) {
	tr, got := collect(t)
	for _, part := range []string{`"one "`, `"two"`} {
		ev := agentEvent{Output: json.RawMessage(part)}
		ev.NodeInfo.Path = "format"
		if err := tr.translate(ev); err != nil {
			t.Fatalf("translate: %v", err)
		}
	}
	if err := tr.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if want := []string{
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END",
	}; !equal(typesOf(*got), want) {
		t.Errorf("event types = %v, want %v", typesOf(*got), want)
	}
	if start, content := (*got)[0].(*events.TextMessageStartEvent), (*got)[1].(*events.TextMessageContentEvent); start.MessageID != content.MessageID {
		t.Errorf("message ids differ: %q vs %q, so the page would split one reply", start.MessageID, content.MessageID)
	}
}

// TestFinishClosesTheTextMessage pins that a stream never ends mid-message.
func TestFinishClosesTheTextMessage(t *testing.T) {
	t.Run("with text open", func(t *testing.T) {
		tr, got := collect(t)
		tr.mustText(t, "hi")
		if err := tr.finish(); err != nil {
			t.Fatalf("finish: %v", err)
		}
		if last := typesOf(*got); last[len(last)-1] != "TEXT_MESSAGE_END" {
			t.Errorf("last event = %q, want TEXT_MESSAGE_END", last[len(last)-1])
		}
	})
	t.Run("with nothing open", func(t *testing.T) {
		tr, got := collect(t)
		if err := tr.finish(); err != nil {
			t.Fatalf("finish: %v", err)
		}
		if len(*got) != 0 {
			t.Errorf("finish emitted %v with nothing open, want nothing", typesOf(*got))
		}
	})
}

func TestNodeName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"first_graph_agent@1/open_refund_case@1", "open_refund_case"},
		{"first_graph_agent@1/format@1", "format"},
		{"prepare", "prepare"},
		{"", "agent_node"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := nodeName(tt.in); got != tt.want {
				t.Errorf("nodeName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestOutputValueDecodesJSONStrings is the bug that shipped first: a JSON string
// put into a chat bubble as raw bytes arrives wrapped in escaped quotes.
func TestOutputValueDecodesJSONStrings(t *testing.T) {
	tests := []struct{ in, want string }{
		{`"Кейс rc-1: статус pending"`, "Кейс rc-1: статус pending"},
		{`{"a":1}`, `{"a":1}`},
		{`null`, ""},
		{``, ""},
		{`"  trimmed  "`, "trimmed"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := outputValue(json.RawMessage(tt.in)); got != tt.want {
				t.Errorf("outputValue(%s) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsJSON(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{`{"a":1}`, true},
		{`[1,2]`, true},
		{`{"broken":`, false}, // valid prefix, invalid JSON
		{`plain text`, false},
		{``, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := isJSON(tt.in); got != tt.want {
				t.Errorf("isJSON(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestLastUserText(t *testing.T) {
	in := types.RunAgentInput{Messages: []types.Message{
		{ID: "1", Role: types.RoleUser, Content: "first"},
		{ID: "2", Role: types.RoleAssistant, Content: "reply"},
		{ID: "3", Role: types.RoleUser, Content: "second"},
	}}
	if got := lastUserText(in); got != "second" {
		t.Errorf("lastUserText = %q, want the most recent user message", got)
	}
	if got := lastUserText(types.RunAgentInput{}); got != "" {
		t.Errorf("lastUserText(no messages) = %q, want empty", got)
	}
}

func TestListenAddress(t *testing.T) {
	tests := []struct {
		port    string
		want    string
		wantErr bool
	}{
		{port: "", want: "127.0.0.1:8081"},
		{port: "  ", want: "127.0.0.1:8081"},
		{port: "8090", want: "0.0.0.0:8090"},
		{port: "0", wantErr: true},
		{port: "70000", wantErr: true},
		{port: "abc", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.port, func(t *testing.T) {
			got, err := listenAddress(tt.port)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("listenAddress(%q) = %q, want an error", tt.port, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("listenAddress(%q) = %v", tt.port, err)
			}
			if got != tt.want {
				t.Errorf("listenAddress(%q) = %q, want %q", tt.port, got, tt.want)
			}
		})
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "x", "y"); got != "x" {
		t.Errorf("firstNonEmpty = %q, want x", got)
	}
	if got := firstNonEmpty("", "  "); got != "" {
		t.Errorf("firstNonEmpty(all blank) = %q, want empty", got)
	}
}

// TestStreamAgentParsesADKFrames pins the SSE reading, using the exact framing
// the ADK service emits: blank-line separated frames, each with a data: line.
func TestStreamAgentParsesADKFrames(t *testing.T) {
	raw := "\r\ndata: {\"output\":{\"case_id\":\"rc-1\"},\"nodeInfo\":{\"path\":\"a@1/open@1\"}}\r\n\r\n" +
		"data: {\"output\":\"done\",\"nodeInfo\":{\"path\":\"a@1/format@1\"}}\r\n\r\n"

	var got []string
	fn := func(ev agentEvent) error {
		got = append(got, strings.TrimSpace(string(ev.Output)))
		return nil
	}
	if err := parseSSE(strings.NewReader(raw), fn); err != nil {
		t.Fatalf("parseSSE: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("parsed %d events (%v), want 2", len(got), got)
	}
	if got[0] != `{"case_id":"rc-1"}` || got[1] != `"done"` {
		t.Errorf("outputs = %v, want the two node outputs in order", got)
	}
}

// mustText is a helper that fails the test instead of returning an error, so the
// table tests above stay readable.
func (t *translator) mustText(tb testing.TB, s string) {
	tb.Helper()
	if err := t.text(s); err != nil {
		tb.Fatalf("text(%q): %v", s, err)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
