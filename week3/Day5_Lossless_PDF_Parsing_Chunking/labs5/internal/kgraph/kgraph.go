// Package kgraph is an in-memory knowledge graph: typed nodes, typed edges,
// and provenance on both.
//
// It is deliberately small — an adjacency map, not a graph database — because
// the lesson is the knowledge model, not graph storage. A process restart
// loses the graph; say so in the README.
//
// The graph is safe for concurrent use.
package kgraph

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Node is one entity. Sources holds the chunk IDs the entity was read from;
// adding the same node again merges Sources instead of overwriting them, so
// provenance survives repeated mentions.
type Node struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Sources []string `json:"sources"`
}

// Edge is a typed relation. Source is the chunk ID it was read from.
type Edge struct {
	From   string `json:"from"`
	Rel    string `json:"rel"`
	To     string `json:"to"`
	Source string `json:"source"`
}

// Graph is the store. The zero value is ready to use.
type Graph struct {
	mu    sync.RWMutex
	nodes map[string]*Node
	edges []Edge
	seen  map[string]bool // From|Rel|To
}

// New returns an empty graph.
func New() *Graph { return &Graph{} }

// AddNode inserts n or merges its Sources into the existing node with the same
// ID. It reports whether a new node was created — the count the lab calls
// NodesAdded.
func (g *Graph) AddNode(n Node) (bool, error) {
	if n.ID == "" {
		return false, errors.New("kgraph: node without id")
	}
	if len(n.Sources) == 0 {
		return false, fmt.Errorf("kgraph: node %q without provenance", n.ID)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.nodes == nil {
		g.nodes = map[string]*Node{}
	}
	if old, ok := g.nodes[n.ID]; ok {
		for _, s := range n.Sources {
			if !slices.Contains(old.Sources, s) {
				old.Sources = append(old.Sources, s)
			}
		}
		return false, nil
	}
	n.Sources = slices.Clone(n.Sources)
	g.nodes[n.ID] = &n
	return true, nil
}

// AddEdge inserts e. Both ends must exist: an edge to an unknown node would
// create an entity nobody extracted. A repeated edge is ignored; the result
// reports whether e was new.
func (g *Graph) AddEdge(e Edge) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, id := range []string{e.From, e.To} {
		if _, ok := g.nodes[id]; !ok {
			return false, fmt.Errorf("kgraph: edge %s -%s-> %s: unknown node %q", e.From, e.Rel, e.To, id)
		}
	}
	key := e.From + "|" + e.Rel + "|" + e.To
	if g.seen[key] {
		return false, nil
	}
	if g.seen == nil {
		g.seen = map[string]bool{}
	}
	g.seen[key] = true
	g.edges = append(g.edges, e)
	return true, nil
}

// Node returns the node with id.
func (g *Graph) Node(id string) (Node, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	n, ok := g.nodes[id]
	if !ok {
		return Node{}, false
	}
	return clone(n), true
}

// ByType returns every node of typ, sorted by ID.
func (g *Graph) ByType(typ string) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []Node
	for _, n := range g.nodes {
		if n.Type == typ {
			out = append(out, clone(n))
		}
	}
	sortNodes(out)
	return out
}

// Neighbors returns the targets of edges from id with relation rel (any
// relation if rel is empty), sorted by ID.
func (g *Graph) Neighbors(id, rel string) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []Node
	for _, e := range g.edges {
		if e.From == id && (rel == "" || e.Rel == rel) {
			out = append(out, clone(g.nodes[e.To]))
		}
	}
	sortNodes(out)
	return out
}

// Stats returns the node and edge counts.
func (g *Graph) Stats() (nodes, edges int) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.nodes), len(g.edges)
}

func clone(n *Node) Node {
	c := *n
	c.Sources = slices.Clone(n.Sources)
	return c
}

func sortNodes(ns []Node) {
	slices.SortFunc(ns, func(a, b Node) int { return strings.Compare(a.ID, b.ID) })
}
