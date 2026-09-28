---
name: adk-go-workflow
description: Use when designing, writing, reviewing or testing ADK Go 2.x (google.golang.org/adk/v2) workflows and agents — choosing an agentic design pattern (single agent, ReAct, chain, fan-out, loop, route, dynamic node, coordinator, hierarchy, swarm, critic, HITL, tool confirmation, ambient), wiring workflow graphs, routes, JoinNode, DynamicNode/RunNode, agent modes, human-in-the-loop resume, or testing agents offline without an API key.
---

# ADK Go Workflow Patterns

Operational guide for building agent systems on **ADK Go v2.4.0**
(`google.golang.org/adk/v2`, станом на 09/2026). Every API name here was read
from the v2.4.0 module source, not from Python or TypeScript docs. The runnable
catalog lives in [`demo/3_adk2_patterns/`](../../../demo/3_adk2_patterns/):
one folder per pattern, each with `main.go`, `main_test.go` and `README.md`.

## When to Use This Skill

- You must pick a shape for an agent system: one agent, a graph, or several agents.
- You write or review code that uses `workflow`, `workflowagent`, `llmagent`,
  `agenttool`, `functiontool`, or HITL (`ResumeOrRequestInput`, tool confirmation).
- A graph fails to build (`workflow.New` / `workflowagent.New` returns an error).
- You need a test that runs an agent offline and deterministically.

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

| ID | Pattern | Go | Core primitives | Demo |
|---|---|---|---|---|
| A1 | Single Agent | ✅ | `llmagent.New` + `Tools` | [a1_single_agent](../../../demo/3_adk2_patterns/a1_single_agent/) |
| A2 | ReAct | 🟡 | `NewDynamicNode` + Go loop + `RunNode`, your step cap | [a2_react](../../../demo/3_adk2_patterns/a2_react/) |
| B1 | Sequential Pipeline | ✅ | `workflow.Chain` | [b1_sequential_pipeline](../../../demo/3_adk2_patterns/b1_sequential_pipeline/) |
| B2 | Fan-Out / Gather | ✅ | `AddFanOut`, `AddFanIn`, `NewJoinNode` | [b2_fan_out_gather](../../../demo/3_adk2_patterns/b2_fan_out_gather/) |
| B3 | Loop | ✅ | routed back edge + your counter | [b3_loop](../../../demo/3_adk2_patterns/b3_loop/) |
| B4 | Conditional Route | ✅ | `ev.Routes`, `AddRoutes`, `Default` | [b4_conditional_route](../../../demo/3_adk2_patterns/b4_conditional_route/) |
| B5 | Custom Logic | 🟡 | `NewDynamicNode`, `RunNode`, `WithRunID` | [b5_custom_logic](../../../demo/3_adk2_patterns/b5_custom_logic/) |
| C1 | Coordinator | ✅ | `SubAgents` in `ModeSingleTurn` / `ModeTask` | [c1_coordinator](../../../demo/3_adk2_patterns/c1_coordinator/) |
| C2 | Hierarchical Decomposition | ✅ | `agenttool.New`, `NewWorkflowNode` | [c2_hierarchical_decomposition](../../../demo/3_adk2_patterns/c2_hierarchical_decomposition/) |
| C3 | Swarm | ❌ | `NewDynamicNode` + shared blackboard | [c3_swarm](../../../demo/3_adk2_patterns/c3_swarm/) |
| C4 | Routed Agent | ❌ | router node + failover you write | [c4_routed_agent](../../../demo/3_adk2_patterns/c4_routed_agent/) |
| D1 | Review and Critique | 🟡 | generator → critic → routed gate | [d1_review_critique](../../../demo/3_adk2_patterns/d1_review_critique/) |
| D2 | Iterative Refinement | ✅ | loop + deterministic checker, keep best | [d2_iterative_refinement](../../../demo/3_adk2_patterns/d2_iterative_refinement/) |
| E1 | Human-in-the-Loop | ✅ | `ResumeOrRequestInput`, `RerunOnResume` | [e1_human_in_the_loop](../../../demo/3_adk2_patterns/e1_human_in_the_loop/) |
| E2 | Tool Confirmation Gate | 🟡 | `functiontool.Config.RequireConfirmationProvider` | [e2_tool_confirmation_gate](../../../demo/3_adk2_patterns/e2_tool_confirmation_gate/) |
| E3 | Ambient Agent | ❌ | deployment wrapper: session per event | [e3_ambient_agent](../../../demo/3_adk2_patterns/e3_ambient_agent/) |
| X | Function Graph | ✅ | function nodes only, no LLM | [x_function_graph](../../../demo/3_adk2_patterns/x_function_graph/) |
| X | Guards | ✅ | build-time graph validation errors | [x_guards](../../../demo/3_adk2_patterns/x_guards/) |

Names that do **not** exist in Go v2.4.0: `RoutedAgent`, `AgentRouter`,
`SecurityPlugin`, `PolicyOutcome`, a `DynamicNode` *type* (only the
`NewDynamicNode` constructor), a `MaxConcurrency` *field* (it is the
`workflow.WithMaxConcurrency(n)` option of `workflow.New`).

## Working Rules

1. **Routes are data on the event.** A function node routes by returning a
   `*session.Event` with `ev.Routes = []string{"label"}`. No route → only `Default` matches.
2. **Every route switch has exactly one `Default`.**
3. **Every loop has a routed back edge AND a counter you wrote.** The validator
   checks the first; nothing checks the second.
4. **Every fan-in goes through `NewJoinNode`.** Its output is
   `map[predecessorName]output` — carry any context you need inside branch outputs.
5. **Side effects happen after a HITL resume, never before the pause.**
6. **Re-entry HITL needs `RerunOnResume: &true`.** `nil` means handoff.
7. **Test offline.** A rule-based model makes every path deterministic; see
   [references/testing.md](references/testing.md).

## References

- [references/api.md](references/api.md) — verified API cheat sheet.
- [references/gotchas.md](references/gotchas.md) — build-time guards and runtime traps.
- [references/testing.md](references/testing.md) — offline tests, HITL answers, coverage gate.
- [examples/function_graph.go](examples/function_graph.go) — function-only graph: fan-out, join, route, bounded loop.
- [examples/hitl_node.go](examples/hitl_node.go) — re-entry human approval node.
- [examples/coordinator.go](examples/coordinator.go) — coordinator with single-turn sub-agents.

Pattern taxonomy and decision tree: `docs/adk/adk-2x-pattern-catalog-ua.html`.
