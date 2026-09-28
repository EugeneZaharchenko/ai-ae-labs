package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

func TestPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"approve, reserve loops to success", "order 1001: 3x keyboard 120", "shipped: order 1001 (3x keyboard) after 3 reserve attempt(s)"},
		{"fraud limit routes to a human", "order 7: 2x gpu 1800", "manual review: order 7"},
		{"out of stock falls to Default", "order 9: 1x unicorn 10", "rejected: order 9"},
		{"lock never frees, cap fires", "order 5: 2x mouse 10", "backordered: order 5 (2x mouse) after 5 reserve attempt(s)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := build(kit.Offline())
			if err != nil {
				t.Fatal(err)
			}
			tr, err := kit.Run(context.Background(), a, io.Discard, tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(tr.Final, tt.want) {
				t.Errorf("final = %q, want prefix %q", tr.Final, tt.want)
			}
			if len(tr.Calls) != 0 {
				t.Errorf("a function graph makes no model/tool calls, got %v", tr.Calls)
			}
		})
	}
}

func TestParseOrderErrors(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"hello", "order 1: 3x keyboard", "order 1: three keyboard 10", "order 1: 3x keyboard ten"} {
		if _, err := parseOrder(nil, in); err == nil {
			t.Errorf("parseOrder(%q) accepted malformed input", in)
		}
	}
}

func TestBadInputFailsTheRun(t *testing.T) {
	t.Parallel()
	a, err := build(kit.Offline())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kit.Run(context.Background(), a, io.Discard, "not an order"); err == nil {
		t.Fatal("want the parse_order error to fail the run")
	}
}

func TestConvert(t *testing.T) {
	t.Parallel()
	var c check
	if err := convert(map[string]any{"name": "stock", "ok": true}, &c); err != nil || c.Name != "stock" || !c.OK {
		t.Fatalf("convert = %+v, %v", c, err)
	}
	if err := convert(func() {}, &c); err == nil {
		t.Fatal("unmarshalable value must fail")
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"route=[retry]", "route=[done]", "checks ⇒"} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("demo trace misses %q:\n%s", w, b.String())
		}
	}
}
