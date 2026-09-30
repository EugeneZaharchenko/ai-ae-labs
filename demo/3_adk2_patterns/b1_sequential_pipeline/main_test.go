package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

func TestPipeline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"full chain", spec.Input, "loaded → SUMMARY: my card was charged TWICE", false},
		{"no Ticket: header keeps whole body", "printer on fire", "loaded → SUMMARY: printer on fire.", false},
		{"empty body fails at extract", "Ticket:   ", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := build(kit.Offline())
			if err != nil {
				t.Fatal(err)
			}
			tr, err := kit.Run(context.Background(), a, io.Discard, tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(tr.Final, tt.want) {
				t.Errorf("final = %q, want prefix %q", tr.Final, tt.want)
			}
		})
	}
}

func TestCleanMasksEmail(t *testing.T) {
	t.Parallel()
	got, err := clean(nil, ticket{Sender: "a@b.io", Body: " write  to a@b.io "})
	if err != nil {
		t.Fatal(err)
	}
	if got.Sender != "[email]" || got.Body != "write to [email]" {
		t.Errorf("clean = %+v", got)
	}
}

func TestSummarizeBrainNonJSON(t *testing.T) {
	t.Parallel()
	if r := summarizeBrain(kit.Prompt{Text: "plain"}); r.Text != "SUMMARY: plain" {
		t.Errorf("reply = %q", r.Text)
	}
}

func TestExtractError(t *testing.T) {
	t.Parallel()
	if _, err := extract(nil, "Ticket:"); err == nil {
		t.Fatal("want error")
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "[email]") {
		t.Errorf("demo trace misses the PII mask:\n%s", b.String())
	}
}
