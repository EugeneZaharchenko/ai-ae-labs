package backend

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory/graphmemorytest"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestConfigFromEnv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		env  map[string]string
		want Config
	}{
		{"defaults", nil, Config{Backend: "dgraph", DgraphAddr: "localhost:9080"}},
		{"explicit", map[string]string{"MEMORY_BACKEND": "dgraph", "DGRAPH_ADDR": "dg:9080"}, Config{Backend: "dgraph", DgraphAddr: "dg:9080"}},
		{"advanced name kept", map[string]string{"MEMORY_BACKEND": "neo4j"}, Config{Backend: "neo4j", DgraphAddr: "localhost:9080"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ConfigFromEnv(env(tc.env)); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

type closer struct{ closed bool }

func (c *closer) Close() error { c.closed = true; return nil }

func TestOpenSelectsDurableBackendsOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := &closer{}
	var dialled string
	ok := func(_ context.Context, addr string) (graphmemory.Store, io.Closer, error) {
		dialled = addr
		return &graphmemorytest.Fake{}, c, nil
	}

	svc, cl, err := open(ctx, ConfigFromEnv(env(nil)), ok)
	if err != nil || svc == nil || cl != c || dialled != "localhost:9080" {
		t.Fatalf("default open = %v, %v, %v (dialled %q)", svc, cl, err, dialled)
	}

	tests := []struct {
		backend string
		want    string
	}{
		{"inmem", "unknown MEMORY_BACKEND"},
		{"neo4j", "++ Advanced backend"},
		{"sqlite", "++ Advanced backend"},
	}
	for _, tc := range tests {
		if _, _, err := open(ctx, Config{Backend: tc.backend}, ok); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("MEMORY_BACKEND=%s: err = %v, want %q", tc.backend, err, tc.want)
		}
	}
}

func TestOpenNamesTheVariableWhenUnreachable(t *testing.T) {
	t.Parallel()
	boom := errors.New("connection refused")
	fail := func(context.Context, string) (graphmemory.Store, io.Closer, error) { return nil, nil, boom }
	_, _, err := open(context.Background(), Config{Backend: "dgraph", DgraphAddr: "x:1"}, fail)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "DGRAPH_ADDR=x:1") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenDgraphUnreachable(t *testing.T) {
	t.Parallel()
	if _, _, err := Open(context.Background(), Config{Backend: "dgraph", DgraphAddr: "127.0.0.1:1"}); err == nil {
		t.Fatal("want an error when nothing listens")
	}
}
