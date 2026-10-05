package kgraph

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func TestAddNode(t *testing.T) {
	t.Parallel()
	var g Graph // zero value is usable
	tests := []struct {
		name      string
		node      Node
		wantAdded bool
		wantErr   bool
	}{
		{"new", Node{ID: "vendor:acme", Type: "Vendor", Name: "Acme", Sources: []string{"c1"}}, true, false},
		{"repeat merges sources", Node{ID: "vendor:acme", Type: "Vendor", Name: "Acme", Sources: []string{"c1", "c7"}}, false, false},
		{"no id", Node{Sources: []string{"c1"}}, false, true},
		{"no provenance", Node{ID: "vendor:x"}, false, true},
	}
	for _, tt := range tests { // sequential: cases share g
		added, err := g.AddNode(tt.node)
		if added != tt.wantAdded || (err != nil) != tt.wantErr {
			t.Errorf("%s: AddNode = %v, %v; want %v, err=%v", tt.name, added, err, tt.wantAdded, tt.wantErr)
		}
	}
	n, ok := g.Node("vendor:acme")
	if !ok || !reflect.DeepEqual(n.Sources, []string{"c1", "c7"}) {
		t.Errorf("Node = %+v, %v; want sources [c1 c7]", n, ok)
	}
	n.Sources[0] = "mutated"
	if again, _ := g.Node("vendor:acme"); again.Sources[0] != "c1" {
		t.Error("Node returned a shared slice")
	}
	if _, ok := g.Node("ghost"); ok {
		t.Error("Node(ghost) found")
	}
}

func TestEdgesAndQueries(t *testing.T) {
	t.Parallel()
	g := New()
	for _, n := range []Node{
		{ID: "vendor:acme", Type: "Vendor", Name: "Acme"},
		{ID: "finding:f-104", Type: "Finding", Name: "F-104"},
		{ID: "finding:f-101", Type: "Finding", Name: "F-101"},
		{ID: "severity:high", Type: "Severity", Name: "High"},
	} {
		n.Sources = []string{"t1"}
		if _, err := g.AddNode(n); err != nil {
			t.Fatal(err)
		}
	}
	edges := []struct {
		e         Edge
		wantAdded bool
		wantErr   bool
	}{
		{Edge{From: "vendor:acme", Rel: "has_finding", To: "finding:f-104", Source: "t1"}, true, false},
		{Edge{From: "vendor:acme", Rel: "has_finding", To: "finding:f-101", Source: "t1"}, true, false},
		{Edge{From: "finding:f-101", Rel: "has_severity", To: "severity:high", Source: "t1"}, true, false},
		{Edge{From: "vendor:acme", Rel: "has_finding", To: "finding:f-104", Source: "t2"}, false, false},
		{Edge{From: "vendor:acme", Rel: "has_finding", To: "ghost"}, false, true},
		{Edge{From: "ghost", Rel: "has_finding", To: "vendor:acme"}, false, true},
	}
	for _, tt := range edges {
		added, err := g.AddEdge(tt.e)
		if added != tt.wantAdded || (err != nil) != tt.wantErr {
			t.Errorf("AddEdge(%+v) = %v, %v; want %v, err=%v", tt.e, added, err, tt.wantAdded, tt.wantErr)
		}
	}
	if n, e := g.Stats(); n != 4 || e != 3 {
		t.Errorf("Stats = %d nodes, %d edges; want 4, 3", n, e)
	}
	ids := func(ns []Node) []string {
		var out []string
		for _, n := range ns {
			out = append(out, n.ID)
		}
		return out
	}
	if got := ids(g.Neighbors("vendor:acme", "has_finding")); !reflect.DeepEqual(got, []string{"finding:f-101", "finding:f-104"}) {
		t.Errorf("Neighbors = %v", got)
	}
	if got := g.Neighbors("vendor:acme", "has_severity"); len(got) != 0 {
		t.Errorf("Neighbors(wrong rel) = %v", got)
	}
	if got := ids(g.Neighbors("finding:f-101", "")); !reflect.DeepEqual(got, []string{"severity:high"}) {
		t.Errorf("Neighbors(any rel) = %v", got)
	}
	if got := ids(g.ByType("Finding")); !reflect.DeepEqual(got, []string{"finding:f-101", "finding:f-104"}) {
		t.Errorf("ByType = %v", got)
	}
}

func TestGraphConcurrent(t *testing.T) {
	t.Parallel()
	g := New()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			_, _ = g.AddNode(Node{ID: fmt.Sprintf("n%d", i%5), Sources: []string{fmt.Sprintf("c%d", i)}})
			g.ByType("")
		})
	}
	wg.Wait()
	if n, _ := g.Stats(); n != 5 {
		t.Errorf("nodes = %d, want 5", n)
	}
}
