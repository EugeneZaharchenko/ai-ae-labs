// Package docling converts a PDF into Markdown through docling-mcp
// (github.com/docling-project/docling-mcp), an MCP server over stdio.
//
// The lab needs one thing from it: a PDF in, Markdown with tables still as
// tables out. Docling does the layout analysis (reading order, table
// structure); this package only carries the request over MCP with the official
// Go SDK (github.com/modelcontextprotocol/go-sdk) and reads the answer.
//
// Two tool calls make one conversion:
//
//  1. convert_document_into_docling_document(source) → {document_key}
//  2. export_docling_document_to_markdown(document_key) → {markdown}
//
// It lives outside the lab package because the student's job is what happens
// to the Markdown (chunking, entities, indexes), not the transport that carries
// it.
package docling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool names exposed by docling-mcp (verified against docling-mcp 3.2.1).
const (
	ToolConvert = "convert_document_into_docling_document"
	ToolExport  = "export_docling_document_to_markdown"
)

// DefaultCommand starts docling-mcp through uvx with the `local` extra, so the
// conversion runs on this machine (the extra pulls docling and its layout
// models — a large first download). Two server defaults are overridden:
//
//   - transport: the default is streamable-http; without `--transport stdio`
//     the process opens an HTTP port and the client waits on stdin forever.
//   - conversion mode and OCR: docling-mcp 3.x defaults to `remote` (a Docling
//     Serve endpoint in DOCLING_MCP_SERVICE_URL); see serverDefaults.
var DefaultCommand = []string{"uvx", "--from=docling-mcp[local]", "docling-mcp-server", "--transport", "stdio"}

// serverDefaults are the server settings this lab needs, applied only when the
// caller's environment does not already set them:
//
//   - DOCLING_MCP_CONVERSION_MODE=local — convert on this machine instead of
//     calling a Docling Serve endpoint (set `remote` plus
//     DOCLING_MCP_SERVICE_URL to use one).
//   - DOCLING_MCP_DO_OCR=false — a PDF with a text layer needs no OCR, and OCR
//     downloads extra models on first use. Set `true` for a scanned PDF.
var serverDefaults = []string{
	"DOCLING_MCP_CONVERSION_MODE=local",
	"DOCLING_MCP_DO_OCR=false",
}

// ConversionEnv returns the environment for the server process: environ plus
// every serverDefaults entry whose variable environ does not set.
func ConversionEnv(environ []string) []string {
	out := append([]string(nil), environ...)
	for _, def := range serverDefaults {
		key := def[:strings.IndexByte(def, '=')+1]
		set := false
		for _, kv := range environ {
			if strings.HasPrefix(kv, key) {
				set = true
				break
			}
		}
		if !set {
			out = append(out, def)
		}
	}
	return out
}

// CommandFromEnv returns the server command. DOCLING_MCP_CMD overrides the
// default with a space-separated command line, for a pinned version
// (`uvx --from=docling-mcp[local]==3.2.1 ...`) or a locally installed binary.
func CommandFromEnv() []string {
	if v := strings.Fields(os.Getenv("DOCLING_MCP_CMD")); len(v) > 0 {
		return v
	}
	return DefaultCommand
}

// Dialer opens an MCP transport to a docling server. Tests pass an in-memory
// transport; production passes a stdio command.
type Dialer func() (mcp.Transport, error)

// CommandDialer starts argv as a child process speaking MCP over stdio.
func CommandDialer(argv []string) Dialer {
	return func() (mcp.Transport, error) {
		if len(argv) == 0 {
			return nil, errors.New("docling: empty server command")
		}
		if _, err := exec.LookPath(argv[0]); err != nil {
			return nil, fmt.Errorf("docling: %q not found — install uv (https://docs.astral.sh/uv/) or set DOCLING_MCP_CMD: %w", argv[0], err)
		}
		// Stderr is inherited: docling logs model downloads there, and a first
		// run that downloads layout models for minutes should not look hung.
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stderr = os.Stderr
		cmd.Env = ConversionEnv(os.Environ())
		return &mcp.CommandTransport{Command: cmd, TerminateDuration: 5 * time.Second}, nil
	}
}

// Client converts documents to Markdown. The zero value is not usable; build
// it with New. It connects lazily on the first conversion, so a run that only
// reads Markdown never starts Python.
type Client struct {
	dial    Dialer
	mu      sync.Mutex
	session *mcp.ClientSession
}

// New returns a client that connects through dial on first use.
func New(dial Dialer) *Client {
	return &Client{dial: dial}
}

// NewFromEnv returns a client for CommandFromEnv().
func NewFromEnv() *Client {
	return New(CommandDialer(CommandFromEnv()))
}

func (c *Client) connect(ctx context.Context) (*mcp.ClientSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		return c.session, nil
	}
	t, err := c.dial()
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "ai-ae-labs-week3", Version: "v1"}, nil)
	s, err := client.Connect(ctx, t, nil)
	if err != nil {
		return nil, fmt.Errorf("docling: connect: %w", err)
	}
	c.session = s
	return s, nil
}

// Close stops the server process, if one was started.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return nil
	}
	err := c.session.Close()
	c.session = nil
	return err
}

// ToMarkdown converts the document at path (local file or URL) to Markdown.
// Local paths are made absolute: the server process resolves them, and its
// working directory is not a contract.
func (c *Client) ToMarkdown(ctx context.Context, path string) (string, error) {
	source := path
	if !strings.Contains(path, "://") {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("docling: resolve %q: %w", path, err)
		}
		source = abs
	}
	s, err := c.connect(ctx)
	if err != nil {
		return "", err
	}
	var conv struct {
		DocumentKey string `json:"document_key"`
	}
	if err := call(ctx, s, ToolConvert, map[string]any{"source": source}, &conv); err != nil {
		return "", err
	}
	if conv.DocumentKey == "" {
		return "", fmt.Errorf("docling: %s returned no document_key", ToolConvert)
	}
	var exp struct {
		Markdown string `json:"markdown"`
	}
	if err := call(ctx, s, ToolExport, map[string]any{"document_key": conv.DocumentKey}, &exp); err != nil {
		return "", err
	}
	return exp.Markdown, nil
}

// call invokes one tool and decodes its structured result into out. FastMCP
// servers send the same object twice — as structuredContent and as a JSON
// text block — so the text is the fallback for servers that send only one.
func call(ctx context.Context, s *mcp.ClientSession, name string, args map[string]any, out any) error {
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return fmt.Errorf("docling: %s: %w", name, err)
	}
	text := textOf(res)
	if res.IsError {
		return fmt.Errorf("docling: %s failed: %s", name, text)
	}
	var raw []byte
	if res.StructuredContent != nil {
		if raw, err = json.Marshal(res.StructuredContent); err != nil {
			return fmt.Errorf("docling: %s: encode result: %w", name, err)
		}
	} else {
		raw = []byte(text)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("docling: %s: decode result %q: %w", name, raw, err)
	}
	return nil
}

func textOf(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}
