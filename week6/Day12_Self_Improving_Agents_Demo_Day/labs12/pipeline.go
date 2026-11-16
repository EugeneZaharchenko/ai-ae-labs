package main

// The full research pipeline as one ADK workflow graph:
//
//	START → plan ─┬→ kb_search ────┐
//	              └→ graph_lookup ─┴→ gather (JoinNode) → collect → draft_report
//	      → critic_loop (dynamic, HW11) → approval_gate (HITL, HW10) → final_report
//
// The strategy is read when the graph is built: it decides the sub-queries,
// the sources, top_k / read_docs, the drafting instruction and the critic
// order. Every node writes one line into the event log (StateDelta key
// "w6p2:<node>"), so a run can be audited node by node.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
)

// Plan is the coordinator's output: what to search and where.
type Plan struct {
	Question   string   `json:"question"`
	SubQueries []string `json:"sub_queries"`
	Sources    []string `json:"sources"`
}

// Branch is one fan-out branch's result.
type Branch struct {
	Question string    `json:"question"`
	Source   string    `json:"source"`
	Enabled  bool      `json:"enabled"`
	Passages []Passage `json:"passages"`
	Note     string    `json:"note,omitempty"`
}

// Evidence is the merged result of all branches.
type Evidence struct {
	Question string    `json:"question"`
	Passages []Passage `json:"passages"`
	Sources  []string  `json:"sources"` // branches that returned something
}

// Draft is a report with its evidence and the critics' state.
type Draft struct {
	Question      string    `json:"question"`
	Report        string    `json:"report"`
	Evidence      Evidence  `json:"evidence"`
	Rounds        int       `json:"rounds"`
	Verdicts      []Verdict `json:"verdicts,omitempty"`
	CriticsPassed bool      `json:"critics_passed"`
}

// PipelineConfig is everything the graph needs.
type PipelineConfig struct {
	Strategy Strategy
	Lib      *Library
	Writer   model.LLM
	Critic   model.LLM
	Gate     *Gate
	Meter    *Meter
}

// AppName is the agent name (and the appName in the session service).
const AppName = "personal_research_agent"

// NewPipeline builds the research agent for one strategy.
func NewPipeline(cfg PipelineConfig) (agent.Agent, error) {
	if cfg.Lib == nil || cfg.Writer == nil || cfg.Critic == nil || cfg.Gate == nil || cfg.Meter == nil {
		return nil, fmt.Errorf("pipeline: неповна конфігурація")
	}
	s := cfg.Strategy
	writer, err := newWriterNode(cfg.Writer, s, cfg.Meter)
	if err != nil {
		return nil, err
	}
	critics, err := newCriticNodes(cfg.Critic, s, cfg.Lib, cfg.Meter)
	if err != nil {
		return nil, err
	}

	plan := workflow.NewEmittingFunctionNode("plan", func(ctx agent.Context, q string, emit func(*session.Event) error) (Plan, error) {
		p := Plan{Question: q, SubQueries: decompose(q, s.MaxSubqueries), Sources: s.Sources}
		return p, emit(trace(ctx, "plan", fmt.Sprintf("%d підзапит(и): %s; джерела: %s",
			len(p.SubQueries), strings.Join(p.SubQueries, " | "), strings.Join(p.Sources, ", "))))
	}, workflow.NodeConfig{})

	kb := workflow.NewEmittingFunctionNode(SourceKB, func(ctx agent.Context, p Plan, emit func(*session.Event) error) (Branch, error) {
		b := kbBranch(cfg.Lib, p, s)
		return b, emit(trace(ctx, SourceKB, branchLine(b)))
	}, workflow.NodeConfig{})

	graph := workflow.NewEmittingFunctionNode(SourceGraph, func(ctx agent.Context, p Plan, emit func(*session.Event) error) (Branch, error) {
		b := graphBranch(cfg.Lib, p, s)
		return b, emit(trace(ctx, SourceGraph, branchLine(b)))
	}, workflow.NodeConfig{})

	gather := workflow.NewJoinNode("gather")

	collect := workflow.NewEmittingFunctionNode("collect", func(ctx agent.Context, in map[string]any, emit func(*session.Event) error) (Evidence, error) {
		ev, err := collectEvidence(in)
		if err != nil {
			return Evidence{}, err
		}
		return ev, emit(trace(ctx, "collect", fmt.Sprintf("%d уривків з %s", len(ev.Passages), strings.Join(ev.Sources, ", "))))
	}, workflow.NodeConfig{})

	draft := workflow.NewDynamicNode("draft_report", func(ctx agent.Context, ev Evidence, emit func(*session.Event) error) (Draft, error) {
		report, err := runWriter(ctx, writer, WriterInput{Question: ev.Question, Evidence: ev.Passages})
		if err != nil {
			return Draft{}, err
		}
		d := Draft{Question: ev.Question, Report: report, Evidence: ev}
		return d, emit(trace(ctx, "draft_report", fmt.Sprintf("чернетка: %d символів, %d цитат", len(report), len(citedIDs(report)))))
	}, workflow.NodeConfig{Timeout: writerTimeout})

	loop := newCriticLoop(s, writer, critics)
	gate := cfg.Gate.Node()

	final := workflow.NewEmittingFunctionNode("final_report", func(ctx agent.Context, g GateResult, emit func(*session.Event) error) (string, error) {
		text := finalText(g)
		return text, emit(trace(ctx, "final_report", fmt.Sprintf("рішення %s (%s)", g.Status, g.By)))
	}, workflow.NodeConfig{})

	eb := workflow.NewEdgeBuilder()
	eb.Add(workflow.Start, plan)
	eb.AddFanOut(plan, kb, graph)
	eb.AddFanIn(gather, kb, graph)
	eb.Add(gather, collect)
	eb.Add(collect, draft)
	eb.Add(draft, loop)
	eb.Add(loop, gate)
	eb.Add(gate, final)
	return workflowagent.New(workflowagent.Config{
		Name:        AppName,
		Description: "Personal Research Agent: план → паралельний збір → чернетка → критики → HITL → звіт.",
		Edges:       eb.Build(),
	})
}

// decompose is the coordinator: the whole question first, then its clauses,
// at most max queries. With max = 1 the question is searched as one blob.
func decompose(q string, max int) []string {
	q = strings.TrimSpace(q)
	out := []string{q}
	if max <= 1 {
		return out
	}
	marked := sentenceRE.ReplaceAllString(q, ".|$1")
	r := strings.NewReplacer("?", "|", ";", "|", ", and ", "|", ", ", "|", " and ", "|")
	for part := range strings.SplitSeq(r.Replace(marked), "|") {
		part = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), "."))
		if len(tokens(part)) < 3 || len(out) >= max || slices.Contains(out, part) || part+"?" == q {
			continue
		}
		out = append(out, part)
	}
	return out
}

// kbBranch: BM25 + rerank per sub-query (top_k each), then the full text of
// the read_docs best documents — what kb_read gives a human researcher.
func kbBranch(lib *Library, p Plan, s Strategy) Branch {
	b := Branch{Question: p.Question, Source: SourceKB, Enabled: true}
	seen := map[string]bool{}
	var docs []string
	for _, q := range p.SubQueries {
		for _, ps := range lib.Search(q, s.TopK) {
			key := ps.DocID + "|" + ps.Text
			if !seen[key] {
				seen[key] = true
				b.Passages = append(b.Passages, ps)
			}
			if !slices.Contains(docs, ps.DocID) {
				docs = append(docs, ps.DocID)
			}
		}
	}
	for _, id := range docs[:min(s.ReadDocs, len(docs))] {
		if full, ok := lib.Read(id); ok {
			b.Passages = append(b.Passages, full)
		}
	}
	return b
}

// graphBranch: entities named in the question → passages that mention them.
func graphBranch(lib *Library, p Plan, s Strategy) Branch {
	if !slices.Contains(s.Sources, SourceGraph) {
		return Branch{Question: p.Question, Source: SourceGraph, Note: "вимкнено стратегією"}
	}
	ps, ents := lib.GraphLookup(p.Question, s.TopK)
	b := Branch{Question: p.Question, Source: SourceGraph, Enabled: true, Passages: ps}
	if len(ents) == 0 {
		b.Note = "у питанні не знайдено сутностей графа"
	} else {
		b.Note = "сутності: " + strings.Join(ents, ", ")
	}
	return b
}

func branchLine(b Branch) string {
	if !b.Enabled {
		return "пропущено: " + b.Note
	}
	ids := map[string]bool{}
	for _, p := range b.Passages {
		ids[p.DocID] = true
	}
	line := fmt.Sprintf("%d уривків з %d документів", len(b.Passages), len(ids))
	if b.Note != "" {
		line += "; " + b.Note
	}
	return line
}

// branchOrder keeps evidence order deterministic: a JoinNode hands over a map.
var branchOrder = []string{SourceKB, SourceGraph}

func collectEvidence(in map[string]any) (Evidence, error) {
	var ev Evidence
	seen := map[string]bool{}
	for _, name := range branchOrder {
		raw, ok := in[name]
		if !ok {
			return Evidence{}, fmt.Errorf("collect: немає гілки %s", name)
		}
		var b Branch
		data, err := json.Marshal(raw)
		if err != nil {
			return Evidence{}, fmt.Errorf("collect: %s: %w", name, err)
		}
		if err := json.Unmarshal(data, &b); err != nil {
			return Evidence{}, fmt.Errorf("collect: %s: %w", name, err)
		}
		if ev.Question == "" {
			ev.Question = b.Question
		}
		if len(b.Passages) > 0 {
			ev.Sources = append(ev.Sources, name)
		}
		for _, p := range b.Passages {
			if key := p.DocID + "|" + p.Text; !seen[key] {
				seen[key] = true
				ev.Passages = append(ev.Passages, p)
			}
		}
	}
	return ev, nil
}

// trace is one event-log line of the pipeline.
func trace(ctx agent.Context, node, line string) *session.Event {
	ev := session.NewEvent(ctx, ctx.InvocationID())
	ev.Actions.StateDelta["w6p2:"+node] = line
	return ev
}
