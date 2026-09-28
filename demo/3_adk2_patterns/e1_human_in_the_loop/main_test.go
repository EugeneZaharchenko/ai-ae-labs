package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

func TestApproval(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		answers    []any
		wantPauses int
		want       string
	}{
		{"approved", []any{"approve"}, 1, "payout of 1200 EUR to A-114 sent"},
		{"rejected", []any{"no way"}, 1, "NOT sent"},
		{"no answer parks the run", nil, 1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := build(kit.Offline())
			if err != nil {
				t.Fatal(err)
			}
			tr, err := kit.Run(context.Background(), a, io.Discard, "Pay out 1200 EUR to merchant A-114", tt.answers...)
			if err != nil {
				t.Fatal(err)
			}
			if tr.Pauses != tt.wantPauses {
				t.Errorf("pauses = %d, want %d", tr.Pauses, tt.wantPauses)
			}
			if tt.want == "" {
				if strings.Contains(tr.Final, "sent") {
					t.Errorf("parked run must not pay out, final = %q", tr.Final)
				}
				return
			}
			if !strings.Contains(tr.Final, tt.want) {
				t.Errorf("final = %q, want %q", tr.Final, tt.want)
			}
		})
	}
}

func TestPrepare(t *testing.T) {
	t.Parallel()
	p, err := prepare(nil, "send money please")
	if err != nil {
		t.Fatal(err)
	}
	if p.Merchant != "unknown" || p.Amount != "unknown" {
		t.Errorf("prepare = %+v, want unknowns", p)
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "asks a human") {
		t.Errorf("demo trace misses the pause:\n%s", b.String())
	}
}
