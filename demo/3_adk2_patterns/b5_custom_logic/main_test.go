package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

func TestCheckout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    []string
		wantErr string
	}{
		{"skips the unknown sku", spec.Input, []string{`"total_cents":2649`, `"lines_ok":3`, `line 3: unknown sku \"unobtainium\"`}, ""},
		{"bad quantity is reported", "ORD-5: 0x widget, 1x gadget", []string{`"total_cents":1299`, "bad quantity"}, ""},
		{"no order id", "1x widget", []string{`"order":"ORD-?"`, `"total_cents":450`}, ""},
		{"no valid line fails the order", "ORD-7: 1x gizmo, 0x widget", nil, "no valid lines"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := build(kit.Offline())
			if err != nil {
				t.Fatal(err)
			}
			tr, err := kit.Run(context.Background(), a, io.Discard, tt.input)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(tr.Final, w) {
					t.Errorf("final = %q, want %q", tr.Final, w)
				}
			}
		})
	}
}

func TestReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"plain error passes through", errors.New("boom"), "boom"},
		{"node failure keeps the child message", fmt.Errorf("%w: unknown sku", workflow.ErrNodeFailed), "unknown sku"},
		{"node failure without marker", workflow.ErrNodeFailed, workflow.ErrNodeFailed.Error()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := reason(tt.err); got != tt.want {
				t.Errorf("reason = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	if strings.Count(b.String(), "price_line ⇒") != 3 {
		t.Errorf("want 3 priced lines in the trace:\n%s", b.String())
	}
}
