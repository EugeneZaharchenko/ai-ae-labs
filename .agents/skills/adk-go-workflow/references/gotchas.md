# ADK Go v2.5.0 — Guards and Gotchas

Two lists: what the framework catches for you, and what it does not.

## Build-time guards (`workflow.New` / `workflowagent.New`)

These fire before any model call. Match them with `errors.Is`.

| Error / check | Trigger | Fix |
|---|---|---|
| `ErrUnconditionalCycle` | A cycle where every edge is unconditional. | Put a route on the back edge (`AddRoute(..., StringRoute("retry"))`). A `Default` edge counts as conditional. |
| `ErrUnsupportedFanIn` (`validateFanIn`) | More than one unconditional incoming edge into a node that is not a `*JoinNode`. | Merge through `workflow.NewJoinNode`. |
| `ErrMultipleDefaultRoutes` | Two `Default` edges from one node. | Keep exactly one. |
| `ErrNodesNotReachable` (`validateConnectivity`) | A node no path from `Start` reaches. | Connect it or remove it. |
| `ErrDuplicateNodeName` (`validateUniqueNames`) | Two different nodes with the same name. | Names are identity; make them unique. |
| `ErrDuplicateEdge` | The same edge twice. | Remove the duplicate. |
| `ErrNoStartNode`, `ErrNodePointsToStart` | No `Start`, or an edge into `Start`. | Begin at `workflow.Start`; never loop back to it. |
| `ErrSubWorkflowNameCollision` | A nested `WorkflowNode` named like its parent. | Rename the sub-graph. |
| `validateNoTaskModeGraphNodes` | An `llmagent` with `Mode: ModeTask` placed as a static graph node. | Use it as a chat sub-agent of a coordinator, or call it via `RunNode` from a dynamic node. |

## Run-time errors

| Error | Meaning |
|---|---|
| `ErrInvalidRunNodeContext` | `RunNode` called outside a dynamic node body. |
| `ErrParallelHITLUnsupported` | A human-input request inside concurrent branches. Put HITL on a sequential path. |
| `ErrOutputAlreadyDelegated` | A second `WithUseAsOutput` child in one parent. |
| `ErrMultipleOutputs` / `ErrMultipleRoutingEvents` | One activation emitted two outputs / two route events. One of each per activation. |
| `ErrInputValidation` | Node input failed its schema. |
| `ErrNothingToResume` | A resume `FunctionResponse` matched no waiting node (wrong ID). |

## Traps nothing catches

1. **Handoff vs re-entry.** `NodeConfig.RerunOnResume` is tri-state. `&true`
   re-runs the paused node from the top, so code after `ResumeOrRequestInput`
   runs. `&false` **and `nil`** (the engine treats nil as handoff) pass the
   human reply to the *successor* as input — the paused node never finishes
   its body. Symptom: the run resumes, no output from your node.
   `NewDynamicNode` defaults to `&true`; function nodes do not.
2. **Side effects before a pause run twice.** A re-entry node executes once
   before the pause and once after. Do the payment/email/write in the next
   node, or after the resume point.
3. **`ResponseSchema` is not enforced.** Validate the human reply yourself.
4. **Loop caps are your code.** The validator proves the back edge is routed;
   it does not count iterations. Always pair a counter with a real exit test.
5. **Join carries only fan-in outputs.** The node after a `JoinNode` gets
   `map[predecessorName]output` and nothing from before the fan-out. Carry the
   context you need inside each branch output.
6. **Join values are `any`.** The map values are whatever each branch
   emitted; do not type-assert a concrete struct. Convert with
   `json.Marshal` + `json.Unmarshal` into the type you need (the demos do this).
7. **No route means Default.** An emitting node that sets no `Routes` takes
   only the `Default` edge. Without a `Default`, an unlabeled case has no
   defined next step — always add one.
8. **Event attribution.** Events of top-level static nodes carry
   `Author` = the workflow name and an empty `NodeInfo.Path`. Nested and dynamic
   children carry `"parent/child@run"` paths. Label nodes in their outputs if a
   trace must show who spoke.
9. **Concurrency limits.** `workflow.WithMaxConcurrency(n)` is an option of
   `workflow.New`; `workflowagent.Config` has no equivalent. It does not gate
   `RunNode` children inside a dynamic node (that would deadlock).
10. **Unique `InterruptID` per run.** A fixed literal lets the Web UI treat a
    new request as already answered. Use `"<prefix>-" + ctx.InvocationID()`.
11. **Mode placement.** An `llmagent` with unset `Mode` behaves as chat when it
    is a sub-agent and as single-turn when it is a graph node. Set `Mode`
    explicitly when the difference matters.
12. **`loopagent.MaxIterations: 0` is an infinite loop, not a no-op.** The
    zero value reads like "default" but means *unbounded*: the loop runs until
    a sub-agent escalates. Nothing else stops it. Always pass a real cap.
13. **The prebuilt `loopagent` cap is checked after a full pass.** With
    `MaxIterations: n` the sub-agent list runs exactly `n` times, not `n-1`.
    Do not also decrement a counter expecting the two to agree.
14. **A prebuilt `parallelagent` cannot fan in.** Branches run in isolated
    contexts (distinct `Branch` names) and cannot read each other's state; the
    results arrive in completion order, not list order. There is no join node
    here. If you need the branches combined, add a following merging agent or
    build the shape as a graph with `AddFanOut` + `NewJoinNode`.
15. **A prebuilt agent accepts no custom `Run`.** All three return an error if
    `AgentConfig.Run` is set — they own the run loop. Wrap them in a graph node
    or a dynamic node instead of trying to override them.
16. **Style is not either/or.** A prebuilt agent can be a graph node
    (`NewAgentNode(seq, cfg)`) and can be driven from a dynamic node
    (`workflow.RunNode(ctx, seq, in)`). Mix per level rather than rewriting a
    working orchestrator by hand.
