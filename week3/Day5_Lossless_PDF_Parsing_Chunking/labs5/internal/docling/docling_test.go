package docling

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type convertIn struct {
	Source string `json:"source"`
}
type convertOut struct {
	FromCache   bool   `json:"from_cache"`
	DocumentKey string `json:"document_key"`
}
type exportIn struct {
	DocumentKey string `json:"document_key"`
}
type exportOut struct {
	DocumentKey string `json:"document_key"`
	Markdown    string `json:"markdown"`
}

// fakeServer imitates docling-mcp's two tools. The sources it saw are
// recorded so a test can check the path the client sent.
type fakeServer struct {
	convertErr bool
	emptyKey   bool
	textOnly   bool // answer with a JSON text block and no structuredContent
	sources    []string
	dials      atomic.Int32
}

func (f *fakeServer) dialer(t *testing.T) Dialer {
	t.Helper()
	return func() (mcp.Transport, error) {
		f.dials.Add(1)
		server := mcp.NewServer(&mcp.Implementation{Name: "fake-docling", Version: "v0"}, nil)
		mcp.AddTool(server, &mcp.Tool{Name: ToolConvert}, func(_ context.Context, _ *mcp.CallToolRequest, in convertIn) (*mcp.CallToolResult, convertOut, error) {
			f.sources = append(f.sources, in.Source)
			if f.convertErr {
				return nil, convertOut{}, errors.New("unsupported format")
			}
			key := "key-1"
			if f.emptyKey {
				key = ""
			}
			return nil, convertOut{DocumentKey: key}, nil
		})
		if f.textOnly {
			server.AddTool(&mcp.Tool{Name: ToolExport, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"markdown":"# from text"}`}}}, nil
			})
		} else {
			mcp.AddTool(server, &mcp.Tool{Name: ToolExport}, func(_ context.Context, _ *mcp.CallToolRequest, in exportIn) (*mcp.CallToolResult, exportOut, error) {
				return nil, exportOut{DocumentKey: in.DocumentKey, Markdown: "# Title\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"}, nil
			})
		}
		ct, st := mcp.NewInMemoryTransports()
		ss, err := server.Connect(context.Background(), st, nil)
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { _ = ss.Close() })
		return ct, nil
	}
}

func TestToMarkdown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		srv      *fakeServer
		path     string
		want     string
		wantErr  string
		wantAbs  bool
		wantDial int32
	}{
		{name: "structured result", srv: &fakeServer{}, path: "doc.pdf", want: "| 1 | 2 |", wantAbs: true, wantDial: 1},
		{name: "text-only result", srv: &fakeServer{textOnly: true}, path: "doc.pdf", want: "# from text", wantAbs: true, wantDial: 1},
		{name: "url passes through", srv: &fakeServer{}, path: "https://example.com/a.pdf", want: "# Title", wantDial: 1},
		{name: "tool error surfaces", srv: &fakeServer{convertErr: true}, path: "doc.pdf", wantErr: "unsupported format"},
		{name: "empty key is an error", srv: &fakeServer{emptyKey: true}, path: "doc.pdf", wantErr: "no document_key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := New(tt.srv.dialer(t))
			t.Cleanup(func() { _ = c.Close() })
			got, err := c.ToMarkdown(context.Background(), tt.path)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ToMarkdown() err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ToMarkdown() error = %v", err)
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("ToMarkdown() = %q, want containing %q", got, tt.want)
			}
			if tt.wantAbs && !filepath.IsAbs(tt.srv.sources[0]) {
				t.Errorf("source sent = %q, want an absolute path", tt.srv.sources[0])
			}
			if !tt.wantAbs && tt.srv.sources[0] != tt.path {
				t.Errorf("source sent = %q, want %q unchanged", tt.srv.sources[0], tt.path)
			}
			if n := tt.srv.dials.Load(); n != tt.wantDial {
				t.Errorf("dials = %d, want %d", n, tt.wantDial)
			}
		})
	}
}

// TestSessionIsReused: one server process serves every document of a run.
func TestSessionIsReused(t *testing.T) {
	t.Parallel()
	srv := &fakeServer{}
	c := New(srv.dialer(t))
	for range 3 {
		if _, err := c.ToMarkdown(context.Background(), "doc.pdf"); err != nil {
			t.Fatal(err)
		}
	}
	if n := srv.dials.Load(); n != 1 {
		t.Errorf("dials = %d, want 1", n)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestDialErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		dial    Dialer
		wantErr string
	}{
		{name: "empty command", dial: CommandDialer(nil), wantErr: "empty server command"},
		{name: "missing binary", dial: CommandDialer([]string{"no-such-docling-binary-xyz"}), wantErr: "install uv"},
		{name: "dialer error", dial: func() (mcp.Transport, error) { return nil, errors.New("boom") }, wantErr: "boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(tt.dial).ToMarkdown(context.Background(), "doc.pdf")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestCommandDialerFindsBinary(t *testing.T) {
	t.Parallel()
	tr, err := CommandDialer([]string{"go", "version"})()
	if err != nil {
		t.Fatalf("CommandDialer: %v", err)
	}
	if _, ok := tr.(*mcp.CommandTransport); !ok {
		t.Fatalf("transport = %T, want *mcp.CommandTransport", tr)
	}
}

func TestConversionEnv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		environ []string
		want    []string
	}{
		{name: "defaults appended", environ: []string{"A=1"},
			want: []string{"A=1", "DOCLING_MCP_CONVERSION_MODE=local", "DOCLING_MCP_DO_OCR=false"}},
		{name: "caller choice kept", environ: []string{"DOCLING_MCP_CONVERSION_MODE=remote", "DOCLING_MCP_DO_OCR=true"},
			want: []string{"DOCLING_MCP_CONVERSION_MODE=remote", "DOCLING_MCP_DO_OCR=true"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ConversionEnv(tt.environ); strings.Join(got, ";") != strings.Join(tt.want, ";") {
				t.Errorf("ConversionEnv() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCommandFromEnv(t *testing.T) {
	t.Setenv("DOCLING_MCP_CMD", "")
	if got := CommandFromEnv(); strings.Join(got, " ") != strings.Join(DefaultCommand, " ") {
		t.Errorf("default = %v", got)
	}
	if !strings.Contains(strings.Join(DefaultCommand, " "), "--transport stdio") {
		t.Error("default command must request stdio transport")
	}
	t.Setenv("DOCLING_MCP_CMD", "docling-mcp-server  --transport stdio")
	if got := CommandFromEnv(); len(got) != 3 || got[0] != "docling-mcp-server" {
		t.Errorf("override = %v", got)
	}
	if NewFromEnv() == nil {
		t.Error("NewFromEnv() = nil")
	}
}

// TestRealServer runs the real docling-mcp on a PDF. It downloads models on
// first use, so it is opt-in:
//
//	DOCLING_E2E=1 go test -run TestRealServer -v ./week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/docling
func TestRealServer(t *testing.T) {
	if os.Getenv("DOCLING_E2E") == "" {
		t.Skip("set DOCLING_E2E=1 to run against the real docling-mcp server")
	}
	c := NewFromEnv()
	t.Cleanup(func() { _ = c.Close() })
	md, err := c.ToMarkdown(context.Background(), filepath.Join("..", "..", "testdata", "ledgerworks_soc2.pdf"))
	if err != nil {
		t.Fatalf("ToMarkdown: %v", err)
	}
	t.Logf("markdown:\n%s", md)
	for _, want := range []string{"Acme Bank JSC", "| F-101", "Severity"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown has no %q:\n%s", want, md)
		}
	}
}
