package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"

	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/ledger"
)

func TestClassifyError(t *testing.T) {
	t.Parallel()
	semantic := &ledger.SemanticError{Field: "merchant_id", Reason: `"undefined" was not extracted`}
	tests := []struct {
		name string
		err  error
		want Class
	}{
		{"semantic error", semantic, ClassSurfaceToModel},
		{"semantic wrapped twice with %w", fmt.Errorf("tool: %w", fmt.Errorf("desk: %w", semantic)), ClassSurfaceToModel},
		{"transient 503", ledger.Unavailable.Err("open_refund_case"), ClassRetryTransient},
		{"transient wrapped", fmt.Errorf("call: %w", ledger.Timeout.Err("open_refund_case")), ClassRetryTransient},
		{"deadline exceeded", fmt.Errorf("acquirer: %w", context.DeadlineExceeded), ClassRetryTransient},
		{"context canceled is a stop, not a retry", context.Canceled, ClassFatal},
		{"canceled wrapped", fmt.Errorf("worker shutting down: %w", context.Canceled), ClassFatal},
		{"temporal canceled", temporal.NewCanceledError(), ClassFatal},
		{"temporal activity timeout", temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_START_TO_CLOSE, nil), ClassRetryTransient},
		{"type string SemanticError", temporal.NewApplicationError("bad id", errTypeSemantic), ClassSurfaceToModel},
		{"type string TransientError", temporal.NewApplicationError("503", errTypeTransient), ClassRetryTransient},
		{"type string FatalError", temporal.NewApplicationError("boom", errTypeFatal), ClassFatal},
		{"unknown type string", temporal.NewApplicationError("?", "SomethingElse"), ClassFatal},
		{"unknown error is not retried blindly", errors.New("disk on fire"), ClassFatal},
		// Text that looks transient must not fool the classifier: only types count.
		{"503 in the text only", errors.New("acquirer returned 503"), ClassFatal},
	}
	for _, tt := range tests {
		if got, _ := classifyError(tt.err); got != tt.want {
			t.Errorf("%s: classifyError(%v) = %s, want %s", tt.name, tt.err, got, tt.want)
		}
	}
}

// The Go type of the error is lost at the Activity boundary; the type string
// and the field must survive it.
func TestToActivityErrorKeepsTypeAndField(t *testing.T) {
	t.Parallel()
	err := toActivityError(fmt.Errorf("desk: %w", &ledger.SemanticError{Field: "merchant_id", Reason: "bad"}))
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) || appErr.Type() != errTypeSemantic {
		t.Fatalf("err = %v, want ApplicationError of type %s", err, errTypeSemantic)
	}
	// Only the ApplicationError, like on the workflow side.
	bare := temporal.NewApplicationErrorWithOptions(appErr.Message(), appErr.Type(),
		temporal.ApplicationErrorOptions{Details: []any{"merchant_id"}})
	if f := errField(bare); f != "merchant_id" {
		t.Errorf("field = %q", f)
	}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{ledger.Unavailable.Err("x"), errTypeTransient},
		{errors.New("unknown"), errTypeFatal},
	} {
		if !errors.As(toActivityError(tc.err), &appErr) || appErr.Type() != tc.want {
			t.Errorf("toActivityError(%v) type = %s, want %s", tc.err, appErr.Type(), tc.want)
		}
	}
	if toActivityError(nil) != nil {
		t.Error("nil must stay nil")
	}
}

func TestBudgetObserve(t *testing.T) {
	t.Parallel()
	b := &budget{}
	semantic := &ledger.SemanticError{Field: "merchant_id", Reason: `"undefined" was not extracted from the request`}
	for i := 1; i <= maxSelfCorrections; i++ {
		obs, err := b.observe("open_refund_case", semantic)
		if err != nil || obs.Status != "invalid_argument" || obs.Field != "merchant_id" || obs.Attempt != i {
			t.Fatalf("correction %d: obs = %+v, err = %v", i, obs, err)
		}
		if !strings.HasPrefix(obs.Observation, "ERROR: ") || obs.asMap()["tool"] != "open_refund_case" {
			t.Errorf("observation = %+v", obs)
		}
	}
	if _, err := b.observe("open_refund_case", semantic); !errors.Is(err, ErrSelfCorrectionExhausted) || b.stop == nil {
		t.Fatalf("4th semantic error: err = %v, want ErrSelfCorrectionExhausted", err)
	}

	b = &budget{}
	obs, err := b.observe("open_refund_case", ledger.Unavailable.Err("open_refund_case"))
	if err != nil || obs.Status != "unavailable" || b.used != 0 {
		t.Errorf("transient after retries: obs = %+v, err = %v, used = %d", obs, err, b.used)
	}
	if _, err := b.observe("search_nodes", context.Canceled); !errors.Is(err, context.Canceled) || b.stop == nil {
		t.Errorf("fatal: err = %v", err)
	}
}

func TestClassString(t *testing.T) {
	t.Parallel()
	for c, want := range map[Class]string{ClassRetryTransient: "retry_transient", ClassSurfaceToModel: "surface_to_model", ClassFatal: "fatal", 9: "Class(9)"} {
		if c.String() != want {
			t.Errorf("%d.String() = %q", int(c), c.String())
		}
	}
}
