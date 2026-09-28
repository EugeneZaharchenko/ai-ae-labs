---
name: adk-go-workflow
description: Use when designing, writing, reviewing or testing ADK Go 2.x (google.golang.org/adk/v2) workflows and agents — choosing between the three workflow styles (graph-based, dynamic, prebuilt sequentialagent/parallelagent/loopagent) and the agentic design pattern (single agent, ReAct, chain, fan-out, loop, route, dynamic node, coordinator, hierarchy, swarm, critic, HITL, tool confirmation, ambient), wiring workflow graphs, routes, JoinNode, DynamicNode/RunNode, agent modes, human-in-the-loop resume, or testing agents offline without an API key.
---

# ADK Go Workflow Patterns

Operational guide for building agent systems on **ADK Go v2.4.0**
(`google.golang.org/adk/v2`, станом на 09/2026). Every API name here was read
from the v2.4.0 module source, not from Python or TypeScript docs. The runnable
catalog lives in [`demo/3_adk2_patterns/`](../../../demo/3_adk2_patterns/):
one folder per pattern, each with `main.go`, `main_test.go` and `README.md`.

## When to Use This Skill

- You must pick a shape for an agent system: one agent, a graph, or several agents.
- You must pick a **style**: graph-based, dynamic, or prebuilt workflow agents.
- You write or review code that uses `workflow`, `workflowagent`, `llmagent`,
  `agenttool`, `functiontool`, or HITL (`ResumeOrRequestInput`, tool confirmation).
- A graph fails to build (`workflow.New` / `workflowagent.New` returns an error).
- You need a test that runs an agent offline and deterministically.

## The Three Workflow Styles

ADK composes multi-step work in three complementary ways. Pick the style first;
it decides which APIs you may touch at all. Then pick the pattern.

| Style | Mechanism | Go APIs | Reach for it when |
|---|---|---|---|
| **Graph-based** | A declarative graph of nodes and edges with explicit routing. | `workflow.New` / `workflowagent.New`, `workflow.Chain`, `NewEdgeBuilder`, `AddRoutes`, `NewJoinNode`, `NewFunctionNode`, `NewAgentNode` | The process is deterministic and structured: the steps and their order are known before the run. |
| **Dynamic** | Programmatic orchestration in plain Go: loops, conditionals, recursion. | `workflow.NewDynamicNode`, `workflow.RunNode`, `WithRunID` | Control flow is too complex or too iterative for a static graph — you would be fighting the builder. |
| **Prebuilt** | Higher-level ready-made orchestrators for the three common shapes. | `sequentialagent.New`, `parallelagent.New`, `loopagent.New` (import path `agent/workflowagents/...`) | Your shape is exactly one of sequence / parallel / loop, and you want no graph assembly at all. |

**Dynamic is the escape hatch, not the default.** Reach for it when a graph
gets unwieldy — not because Go control flow is more familiar. A graph you can
read is worth more than a dynamic node only you can follow.

**The styles are not exclusive.** They nest: a graph node can wrap a prebuilt
agent (`NewAgentNode(sequentialagent.New(...))`), and a dynamic node can drive
any of them through `RunNode`. Mix them per level.

**On "superseded".** The ADK docs say that from 2.0, template workflows are
superseded by graph-based and dynamic workflows — for Python and Go. That is a
direction of travel, not a removal: the Go prebuilt agents are present and
working in v2.4.0. Prefer a graph when you need routing, fan-in, retries or
HITL; the prebuilt three carry none of that.

### Prebuilt workflow agents (Go v2.4.0)

All three verified to run offline in v2.4.0. Each takes
`Config{AgentConfig: agent.Config{Name, SubAgents}}`.

| Constructor | Behaviour | Go caveats |
|---|---|---|
| `sequentialagent.New` | Runs sub-agents once each, in list order. | Shares one session; the final sub-agent's text is the result. |
| `parallelagent.New` | Runs sub-agents concurrently in isolated branches. | **No fan-in.** Each branch writes to its own branch state; results arrive in completion order, not list order. To gather, follow it with a merging agent or use graph `AddFanOut` + `NewJoinNode` instead. |
| `loopagent.New` | Repeats the sub-agent list. | `Config.MaxIterations uint`. **`0` means run forever** — the loop exits only when a sub-agent escalates. Always set a cap *and* an exit test. |

An escalation is `ctx.Actions().Escalate = true` from a tool body; the loop
checks it after each sub-agent and stops. There is no other stop signal.

**`agent/workflowagent` (singular) is not `agent/workflowagents/` (plural).**
The singular package adapts a `*Workflow` graph into an `agent.Agent`; the
plural directory holds the three prebuilt agents. A wrong import still
compiles only if the symbol exists — here it does not, so the typo surfaces as
a build error, not a silent bug. Read the path twice.

## The One Axis: Who Decides the Next Step?

Position on this axis predicts the bill better than anything else. To the right,
more model calls go to *deciding* instead of *doing*.

```
code decides                                                        model decides
B1 · B2 · B4 · B5 ─ B3 · D1 · D2 ─ E1 · E2 · E3 (human/event) ─ A1 · A2 ─ C1 · C2 ─ C3
```

**Rule of motion:** start on the left. Move right only when the pattern on the
left was measured to fail — not when it looks too simple.

## Decision Tree

1. Can one optimized LLM call + retrieval do it? → **Do not build an agent.**
2. Is the sequence of steps known and fixed?
   - yes → independent steps? **B2** fan-out : **B1** chain.
     Add **B4** for branching, **B3** for "repeat until", **B5** when the graph
     gets in the way of plain Go control flow.
   - no → are request categories enumerable? **C1** coordinator (try **B4** first — cheaper).
     Not enumerable → multi-level planning? **C2** hierarchy : **A2** ReAct.
3. Must conflicting viewpoints argue to reach a good answer? → **C3** swarm. Justify it.
4. Modifiers on top of anything: must pass a bar → **D1**; improves with passes
   → **D2**; a named human signs off → **E1**; one irreversible action → **E2**;
   failover / A-B / model tiers → **C4**.
5. Is anyone waiting for the answer? No → wrap it all in **E3** ambient.

## Pattern Index (Go v2.4.0)

✅ primitive exists · 🟡 compose from primitives · ❌ no Go API — build by hand

`Style` is which of the three workflow styles the demo uses: **graph**
(`workflowagent.New` + edges), **dyn** (`NewDynamicNode` + `RunNode`),
**pre** (prebuilt `*agent`), or **none** (no graph at all).

| ID | Pattern | Go | Style | Core primitives | Demo |
|---|---|---|---|---|---|
| A1 | Single Agent | ✅ | none | `llmagent.New` + `Tools` | [a1_single_agent](../../../demo/3_adk2_patterns/a1_single_agent/) |
| A2 | ReAct | 🟡 | dyn | `NewDynamicNode` + Go loop + `RunNode`, your step cap | [a2_react](../../../demo/3_adk2_patterns/a2_react/) |
| B1 | Sequential Pipeline | ✅ | graph | `workflow.Chain` (prebuilt `sequentialagent.New` also fits) | [b1_sequential_pipeline](../../../demo/3_adk2_patterns/b1_sequential_pipeline/) |
| B2 | Fan-Out / Gather | ✅ | graph | `AddFanOut`, `AddFanIn`, `NewJoinNode` (prebuilt `parallelagent` has no fan-in) | [b2_fan_out_gather](../../../demo/3_adk2_patterns/b2_fan_out_gather/) |
| B3 | Loop | ✅ | graph | routed back edge + your counter (prebuilt `loopagent` also fits) | [b3_loop](../../../demo/3_adk2_patterns/b3_loop/) |
| B4 | Conditional Route | ✅ | graph | `ev.Routes`, `AddRoutes`, `Default` | [b4_conditional_route](../../../demo/3_adk2_patterns/b4_conditional_route/) |
| B5 | Custom Logic | 🟡 | dyn | `NewDynamicNode`, `RunNode`, `WithRunID` | [b5_custom_logic](../../../demo/3_adk2_patterns/b5_custom_logic/) |
| C1 | Coordinator | ✅ | none | `SubAgents` in `ModeSingleTurn` / `ModeTask` | [c1_coordinator](../../../demo/3_adk2_patterns/c1_coordinator/) |
| C2 | Hierarchical Decomposition | ✅ | none | `agenttool.New`, `NewWorkflowNode` | [c2_hierarchical_decomposition](../../../demo/3_adk2_patterns/c2_hierarchical_decomposition/) |
| C3 | Swarm | ❌ | dyn | `NewDynamicNode` + shared blackboard | [c3_swarm](../../../demo/3_adk2_patterns/c3_swarm/) |
| C4 | Routed Agent | ❌ | dyn | router node + failover you write | [c4_routed_agent](../../../demo/3_adk2_patterns/c4_routed_agent/) |
| D1 | Review and Critique | 🟡 | graph | generator → critic → routed gate | [d1_review_critique](../../../demo/3_adk2_patterns/d1_review_critique/) |
| D2 | Iterative Refinement | ✅ | dyn | loop + deterministic checker, keep best | [d2_iterative_refinement](../../../demo/3_adk2_patterns/d2_iterative_refinement/) |
| E1 | Human-in-the-Loop | ✅ | graph | `ResumeOrRequestInput`, `RerunOnResume` | [e1_human_in_the_loop](../../../demo/3_adk2_patterns/e1_human_in_the_loop/) |
| E2 | Tool Confirmation Gate | 🟡 | none | `functiontool.Config.RequireConfirmationProvider` | [e2_tool_confirmation_gate](../../../demo/3_adk2_patterns/e2_tool_confirmation_gate/) |
| E3 | Ambient Agent | ❌ | graph | deployment wrapper: session per event | [e3_ambient_agent](../../../demo/3_adk2_patterns/e3_ambient_agent/) |
| X | Function Graph | ✅ | graph | function nodes only, no LLM | [x_function_graph](../../../demo/3_adk2_patterns/x_function_graph/) |
| X | Guards | ✅ | graph | build-time graph validation errors | [x_guards](../../../demo/3_adk2_patterns/x_guards/) |

The catalog has **no prebuilt-style demo** at all: `sequentialagent`,
`parallelagent` and `loopagent` appear in none of the 18 folders. B1, B2 and B3
are the same three shapes built as graphs. Read B1/B3 for the graph form and
this table for when the prebuilt form is enough.

Names that do **not** exist in Go v2.4.0: `RoutedAgent`, `AgentRouter`,
`SecurityPlugin`, `PolicyOutcome`, a `DynamicNode` *type* (only the
`NewDynamicNode` constructor), a `MaxConcurrency` *field* (it is the
`workflow.WithMaxConcurrency(n)` option of `workflow.New`).

## Working Rules

1. **Pick the style before the pattern.** Graph if the steps are known; dynamic
   if the control flow fights a static graph; prebuilt if the shape is exactly
   sequence / parallel / loop. See [The Three Workflow Styles](#the-three-workflow-styles).
2. **Routes are data on the event.** A function node routes by returning a
   `*session.Event` with `ev.Routes = []string{"label"}`. No route → only `Default` matches.
3. **Every route switch has exactly one `Default`.**
4. **Every loop has a routed back edge AND a counter you wrote.** The validator
   checks the first; nothing checks the second. With prebuilt `loopagent` the
   cap is `MaxIterations` — and `0` means *unbounded*, not *skip*.
5. **Every fan-in goes through `NewJoinNode`.** Its output is
   `map[predecessorName]output` — carry any context you need inside branch outputs.
   Prebuilt `parallelagent` has no fan-in at all: add a merger or use the graph.
6. **Side effects happen after a HITL resume, never before the pause.**
7. **Re-entry HITL needs `RerunOnResume: &true`.** `nil` means handoff.
8. **Test offline.** A rule-based model makes every path deterministic; see
   [references/testing.md](references/testing.md).

## References

- [references/api.md](references/api.md) — verified API cheat sheet.
- [references/gotchas.md](references/gotchas.md) — build-time guards and runtime traps.
- [references/testing.md](references/testing.md) — offline tests, HITL answers, coverage gate.
- [examples/function_graph.go](examples/function_graph.go) — function-only graph: fan-out, join, route, bounded loop.
- [examples/hitl_node.go](examples/hitl_node.go) — re-entry human approval node.
- [examples/coordinator.go](examples/coordinator.go) — coordinator with single-turn sub-agents.
- [examples/prebuilt_agents.go](examples/prebuilt_agents.go) — the three prebuilt orchestrators.

Pattern taxonomy and decision tree: `docs/adk/adk-2x-pattern-catalog-ua.html`.
Workflow styles and the Go API: <https://adk.dev/graphs/>.
