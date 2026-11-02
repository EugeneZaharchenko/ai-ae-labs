package main

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/temporal"

	"github.com/dimetron/ai-eng-course/labs/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/internal/ledger"
)

// Class is what the harness does with an error.
type Class int

const (
	ClassRetryTransient Class = iota + 1 // give it to the engine: a retry can help
	ClassSurfaceToModel                  // return it to the model as an Observation: a retry cannot help
	ClassFatal                           // stop
)

func (c Class) String() string {
	switch c {
	case ClassRetryTransient:
		return "retry_transient"
	case ClassSurfaceToModel:
		return "surface_to_model"
	case ClassFatal:
		return "fatal"
	}
	return fmt.Sprintf("Class(%d)", int(c))
}

// maxSelfCorrections is how many semantic errors one turn may turn into
// Observations before the harness stops the run.
const maxSelfCorrections = 3

// ErrSelfCorrectionExhausted is wrapped by the error that ends such a run.
var ErrSelfCorrectionExhausted = errors.New("self-correction budget exhausted")

// Error types of temporal.ApplicationError. Only this string crosses the
// worker → workflow boundary (the Go type of the error does not), so the
// workflow side classifies by it.
const (
	errTypeSemantic  = "SemanticError"
	errTypeTransient = "TransientError"
	errTypeFatal     = "FatalError"
	// errTypeExhausted is the type of the workflow error when the
	// self-correction budget is spent.
	errTypeExhausted = "SelfCorrectionExhausted"
)

// classifyError decides the class with errors.Is / errors.As only, never by
// the text of the error. It works on both sides of the Activity boundary:
// in the worker it sees ledger errors, in the workflow it sees Temporal
// errors whose ApplicationError carries the type string.
func classifyError(err error) (Class, string) {
	if err == nil {
		return ClassFatal, "nil error"
	}
	var canceled *temporal.CanceledError
	if errors.Is(err, context.Canceled) || errors.As(err, &canceled) {
		return ClassFatal, "stopped: " + err.Error()
	}
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		switch appErr.Type() {
		case errTypeSemantic:
			return ClassSurfaceToModel, appErr.Message()
		case errTypeTransient:
			return ClassRetryTransient, appErr.Message()
		}
		return ClassFatal, appErr.Message()
	}
	var semantic *ledger.SemanticError
	if errors.As(err, &semantic) || errors.Is(err, ledger.ErrSemantic) {
		return ClassSurfaceToModel, err.Error()
	}
	var timeout *temporal.TimeoutError
	if errors.Is(err, ledger.ErrTransient) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) {
		return ClassRetryTransient, err.Error()
	}
	// An unknown error is not retried blindly.
	return ClassFatal, err.Error()
}

// toActivityError is the worker side of the boundary: it turns a ledger error
// into an ApplicationError whose type says the class. The RetryPolicy of the
// Activity decides by this type what Temporal repeats.
func toActivityError(err error) error {
	if err == nil {
		return nil
	}
	class, msg := classifyError(err)
	switch class {
	case ClassSurfaceToModel:
		return temporal.NewApplicationErrorWithOptions(msg, errTypeSemantic,
			temporal.ApplicationErrorOptions{Cause: err, Details: []any{errField(err)}})
	case ClassRetryTransient:
		return temporal.NewApplicationErrorWithOptions(msg, errTypeTransient, temporal.ApplicationErrorOptions{Cause: err})
	}
	return temporal.NewApplicationErrorWithOptions(msg, errTypeFatal, temporal.ApplicationErrorOptions{Cause: err})
}

// errField returns the wrong argument: from the ledger error in the worker,
// or from the ApplicationError details in the workflow.
func errField(err error) string {
	var semantic *ledger.SemanticError
	if errors.As(err, &semantic) {
		return semantic.Field
	}
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) && appErr.HasDetails() {
		var field string
		if appErr.Details(&field) == nil {
			return field
		}
	}
	return ""
}

// Observation is what the model reads after a call that failed.
type Observation struct {
	Tool        string `json:"tool"`
	Status      string `json:"status"` // invalid_argument | unavailable
	Field       string `json:"field,omitempty"`
	Observation string `json:"observation"`
	Attempt     int    `json:"attempt,omitempty"`
}

func (o Observation) asMap() map[string]any {
	m := map[string]any{"tool": o.Tool, "status": o.Status, "observation": o.Observation}
	if o.Field != "" {
		m["field"] = o.Field
	}
	if o.Attempt > 0 {
		m["attempt"] = o.Attempt
	}
	return m
}

// budget counts self-corrections of one turn. It lives in the turn (created
// inside the workflow run), not in a global: a global counter breaks parallel
// tests and mixes turns of different users.
type budget struct {
	used int
	// stop is set when a tool hit a fatal error or the budget is spent; the
	// workflow ends the turn on it. ADK would only show a tool error to the
	// model as {"error": ...} and keep going.
	stop error
}

// observe turns a tool error into an Observation for the model, or into an
// error that ends the turn.
func (b *budget) observe(tool string, err error) (Observation, error) {
	class, msg := classifyError(err)
	switch class {
	case ClassSurfaceToModel:
		b.used++
		if b.used > maxSelfCorrections {
			b.stop = fmt.Errorf("%s: %d semantic errors in one turn, last: %s: %w", tool, b.used, msg, ErrSelfCorrectionExhausted)
			return Observation{}, b.stop
		}
		return Observation{
			Tool: tool, Status: "invalid_argument", Field: errField(err), Attempt: b.used,
			Observation: "ERROR: " + msg + " — please correct your arguments and try again",
		}, nil
	case ClassRetryTransient:
		// The engine already retried it; repeating the call from the model
		// would only start the same retries again.
		return Observation{
			Tool: tool, Status: "unavailable",
			Observation: "ERROR: " + msg + " — still failing after retries; do not call it again, tell the user",
		}, nil
	}
	b.stop = fmt.Errorf("%s: fatal: %w", tool, err)
	return Observation{}, b.stop
}
