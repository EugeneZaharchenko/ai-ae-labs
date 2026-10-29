package billing

import (
	"context"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/gate"
)

// TestTaskDataIsValid grades every task's reference and buggy variants with the
// real toolchain. It is the eval's own test-validity check: a reference that
// fails, or a "buggy" variant that survives the holdout, makes every number the
// eval prints meaningless.
func TestTaskDataIsValid(t *testing.T) {
	t.Parallel()
	var r gate.Runner
	for _, task := range Tasks() {
		t.Run(task.ID, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			for name, test := range map[string]string{"visible": task.Visible, "holdout": task.Holdout} {
				if rep := r.Grade(ctx, gate.Spec{Code: task.Reference, Test: test}); !rep.OK {
					t.Errorf("reference vs %s: stage %s\n%s", name, rep.Stage, rep.Output)
				}
			}
			if rep := r.Grade(ctx, gate.Spec{Code: task.Buggy, Test: task.Visible}); !rep.OK {
				t.Errorf("buggy must pass the visible tests (else the loop would catch it): stage %s\n%s", rep.Stage, rep.Output)
			}
			if rep := r.Grade(ctx, gate.Spec{Code: task.Buggy, Test: task.Holdout}); rep.OK {
				t.Error("buggy passes the holdout: the holdout does not catch what it exists to catch")
			}
		})
	}
}

func TestByIDAndTasksCopy(t *testing.T) {
	t.Parallel()
	if _, ok := ByID("nope"); ok {
		t.Error("ByID(nope) found a task")
	}
	got, ok := ByID("prorate_refund")
	if !ok || got.Prompt == "" {
		t.Fatalf("ByID(prorate_refund) = %+v, %v", got, ok)
	}
	a := Tasks()
	a[0].ID = "mutated"
	if Tasks()[0].ID == "mutated" {
		t.Error("Tasks() returns shared state")
	}
}
