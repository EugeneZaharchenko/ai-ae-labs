package main

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

func TestReviewCritique(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"good draft is published", "Tell the customer that order ORD-42 is delayed", "published: Sorry, order ORD-42"},
		{"known-bad draft is sent back", "Promise ORD-42 arrives tomorrow", "sent back to the writer, failed C3"},
		{"missing id is sent back", "tell them it is late", "failed C1"},
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
			if !strings.Contains(tr.Final, tt.want) {
				t.Errorf("final = %q, want %q", tr.Final, tt.want)
			}
		})
	}
}

func TestReview(t *testing.T) {
	t.Parallel()
	long := "ORD-1 " + strings.Repeat("word ", 40)
	tests := []struct {
		draft string
		want  verdict
	}{
		{"Order ORD-7 is late.", verdict{Approve: true}},
		{"It is late.", verdict{Failed: []string{"C1"}}},
		{long, verdict{Failed: []string{"C2"}}},
		{"ORD-7 guaranteed tomorrow", verdict{Failed: []string{"C3"}}},
	}
	for _, tt := range tests {
		if got := review(tt.draft); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("review(%q) = %+v, want %+v", tt.draft, got, tt.want)
		}
	}
}

func TestParseVerdict(t *testing.T) {
	t.Parallel()
	tests := []struct {
		text string
		want bool
	}{
		{`{"approve": true}`, true},
		{"Sure! ```json\n{\"approve\": true}\n```", true},
		{`{"approve": false, "failed": ["C2"]}`, false},
		{"LGTM", false},
	}
	for _, tt := range tests {
		if got := parseVerdict(tt.text).Approve; got != tt.want {
			t.Errorf("parseVerdict(%q).Approve = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestExecuteDemo(t *testing.T) {
	var b strings.Builder
	if err := kit.Execute(context.Background(), spec, nil, &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "route=[true]") {
		t.Errorf("demo trace misses the approve route:\n%s", b.String())
	}
}
