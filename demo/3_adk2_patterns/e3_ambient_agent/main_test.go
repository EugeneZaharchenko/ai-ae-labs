package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

func TestBody(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{"outage is P1", "Checkout returns 500", "filed as P1", nil},
		{"degraded is P2", "search is slow", "filed as P2", nil},
		{"question is P3", "how do I export?", "filed as P3", nil},
		{"empty event fails loudly", "   ", "", errEmptyTicket},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := build(kit.Offline())
			if err != nil {
				t.Fatal(err)
			}
			tr, err := kit.Run(context.Background(), a, io.Discard, tt.input)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tr.Final != tt.want {
				t.Errorf("final = %q, want %q", tr.Final, tt.want)
			}
		})
	}
}

func TestFileTicket(t *testing.T) {
	t.Parallel()
	for in, wantErr := range map[string]bool{"P1": false, " p3 ": false, "P": true, "P9": true, "urgent": true} {
		_, err := fileTicket(nil, in)
		if (err != nil) != wantErr {
			t.Errorf("fileTicket(%q) err = %v, wantErr %v", in, err, wantErr)
		}
	}
}

func TestReport(t *testing.T) {
	t.Parallel()
	results := []outcome{
		{Ticket: inbox[0], Result: "filed as P1"},
		{Ticket: inbox[1], Err: errors.New("model quota exceeded")},
		// inbox[2] and inbox[3] never came back
	}
	var b strings.Builder
	if err := report(&b, results); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"summary: 4 events, 1 ok, 3 failed",
		"T-2: model quota exceeded",
		"T-3: never processed",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("report misses %q:\n%s", want, b.String())
		}
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "summary: 4 events, 3 ok, 1 failed (T-3: empty ticket body)") {
		t.Errorf("demo summary is wrong:\n%s", b.String())
	}
}

func TestDemoCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var b strings.Builder
	if err := demo(ctx, kit.Offline(), &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "never processed") {
		t.Errorf("a cancelled drain must report unprocessed events:\n%s", b.String())
	}
}
