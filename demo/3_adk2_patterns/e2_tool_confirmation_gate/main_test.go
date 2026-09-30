package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

func TestConfirmationGate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		input      string
		answers    []any
		wantPauses int
		want       string
	}{
		{"prod delete confirmed", "Delete bucket prod-logs", []any{true}, 1, "Bucket prod-logs deleted."},
		{"prod delete rejected", "Delete bucket prod-logs", []any{false}, 1, "NOT deleted"},
		{"prod delete parks without a human", "Delete bucket prod-logs", nil, 1, ""},
		{"non-prod delete needs no confirmation", "Delete bucket tmp-cache", nil, 0, "Bucket tmp-cache deleted."},
		{"unknown bucket is reported", "Delete bucket old-stuff", nil, 0, "NOT deleted"},
		{"no bucket asks back", "clean up storage", nil, 0, "Which bucket?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := build(kit.Offline())
			if err != nil {
				t.Fatal(err)
			}
			tr, err := kit.Run(context.Background(), a, io.Discard, tt.input, tt.answers...)
			if err != nil {
				t.Fatal(err)
			}
			if tr.Pauses != tt.wantPauses {
				t.Errorf("pauses = %d, want %d", tr.Pauses, tt.wantPauses)
			}
			if !strings.Contains(tr.Final, tt.want) {
				t.Errorf("final = %q, want %q", tr.Final, tt.want)
			}
		})
	}
}

func TestNeedsConfirmation(t *testing.T) {
	t.Parallel()
	for bucket, want := range map[string]bool{"prod-logs": true, "tmp-cache": false} {
		if got := needsConfirmation(deleteArgs{Bucket: bucket}); got != want {
			t.Errorf("needsConfirmation(%q) = %v, want %v", bucket, got, want)
		}
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "adk_request_confirmation") {
		t.Errorf("demo trace misses the confirmation:\n%s", b.String())
	}
}
