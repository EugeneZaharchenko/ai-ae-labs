//go:build integration

package dgraph

import (
	"context"
	"os"
	"testing"

	"github.com/dgraph-io/dgo/v250/protos/api"

	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory/graphmemorytest"
)

// TestConformance runs the shared suite against a real Dgraph:
//
//	docker compose -f week4/Day8_Fault_Tolerant_Tool_Harness/labs8/compose.yaml up -d dgraph
//	DGRAPH_TEST_ADDR=localhost:9080 go test -tags integration ./week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory/dgraph/
//
// Each case starts from an empty graph: the test DELETES ALL DATA in the
// target Dgraph. That is why it reads DGRAPH_TEST_ADDR, never DGRAPH_ADDR —
// pointing it at the lab's memory by accident would wipe the memory.
func TestConformance(t *testing.T) {
	addr := os.Getenv("DGRAPH_TEST_ADDR")
	if addr == "" {
		t.Skip("set DGRAPH_TEST_ADDR to a Dgraph whose data may be deleted")
	}
	ctx := context.Background()
	graphmemorytest.Run(t, graphmemorytest.Harness{
		Open: func(t *testing.T) graphmemory.Store {
			t.Helper()
			s, err := Open(ctx, addr)
			if err != nil {
				t.Fatalf("Open %s: %v (is the dgraph container up?)", addr, err)
			}
			if err := s.c.Alter(ctx, &api.Operation{DropOp: api.Operation_DATA}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
		// Reopen drops the connection and dials a new one: whatever is read
		// back lives in Dgraph, not in this process.
		Reopen: func(t *testing.T, old graphmemory.Store) graphmemory.Store {
			t.Helper()
			_ = old.(*Store).Close()
			s, err := Open(ctx, addr)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
	})
}
