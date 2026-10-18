package graphmemorytest_test

import (
	"context"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory"
	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/graphmemory/graphmemorytest"
)

func TestFakePassesConformance(t *testing.T) {
	t.Parallel()
	graphmemorytest.Run(t, graphmemorytest.Harness{
		Open:   func(*testing.T) graphmemory.Store { return &graphmemorytest.Fake{} },
		Reopen: func(_ *testing.T, s graphmemory.Store) graphmemory.Store { return s },
	})
}

func TestFakeRejectsUnscopedQuery(t *testing.T) {
	t.Parallel()
	var f graphmemorytest.Fake // zero value is usable
	if _, err := f.Search(context.Background(), graphmemory.Query{}); err == nil {
		t.Fatal("want ErrScope for a query without app and user")
	}
}
