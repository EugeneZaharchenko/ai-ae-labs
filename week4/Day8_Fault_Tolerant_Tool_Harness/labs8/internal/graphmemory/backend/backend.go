// Package backend opens the graphmemory store named by the environment.
//
// It lives apart from graphmemory because it imports the backends, and the
// backends import graphmemory.
//
// Only durable stores can be named. MEMORY_BACKEND defaults to dgraph; any
// other value is an error that says what to do — never a silent fallback to
// something that forgets on restart.
package backend

import (
	"context"
	"fmt"
	"io"

	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory/dgraph"
)

// Backend names.
const (
	Dgraph = "dgraph"
)

// Default is the backend used when MEMORY_BACKEND is empty.
const Default = Dgraph

// advanced lists backends the course leaves as ++ Advanced work: they are
// valid names, but no implementation ships in core.
var advanced = []string{"neo4j", "memgraph", "falkordb", "postgres", "sqlite"}

// Config is the memory configuration, usually from ConfigFromEnv.
type Config struct {
	Backend    string // MEMORY_BACKEND
	DgraphAddr string // DGRAPH_ADDR
}

// ConfigFromEnv reads the configuration through getenv (os.Getenv in
// production, a map in tests) and applies defaults.
func ConfigFromEnv(getenv func(string) string) Config {
	c := Config{Backend: getenv("MEMORY_BACKEND"), DgraphAddr: getenv("DGRAPH_ADDR")}
	if c.Backend == "" {
		c.Backend = Default
	}
	if c.DgraphAddr == "" {
		c.DgraphAddr = "localhost:9080"
	}
	return c
}

// Opener connects to a Dgraph alpha; tests replace it.
type Opener func(ctx context.Context, addr string) (graphmemory.Store, io.Closer, error)

// OpenDgraph is the real Opener.
func OpenDgraph(ctx context.Context, addr string) (graphmemory.Store, io.Closer, error) {
	s, err := dgraph.Open(ctx, addr)
	if err != nil {
		return nil, nil, err
	}
	return s, s, nil
}

// Open returns a memory Service over the configured durable store, and the
// closer that releases it.
func Open(ctx context.Context, c Config) (*graphmemory.Service, io.Closer, error) {
	return open(ctx, c, OpenDgraph)
}

func open(ctx context.Context, c Config, dial Opener) (*graphmemory.Service, io.Closer, error) {
	switch c.Backend {
	case Dgraph:
		s, closer, err := dial(ctx, c.DgraphAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("graphmemory: MEMORY_BACKEND=dgraph: cannot reach DGRAPH_ADDR=%s (is `docker compose up -d dgraph` running?): %w", c.DgraphAddr, err)
		}
		return graphmemory.New(s), closer, nil
	}
	for _, a := range advanced {
		if c.Backend == a {
			return nil, nil, fmt.Errorf("graphmemory: MEMORY_BACKEND=%s is a ++ Advanced backend and is not built into this binary; implement graphmemory.Store, pass graphmemorytest.Run, and register it in labs8/internal/graphmemory/backend", a)
		}
	}
	return nil, nil, fmt.Errorf("graphmemory: unknown MEMORY_BACKEND=%q (durable backends: %s)", c.Backend, Dgraph)
}
