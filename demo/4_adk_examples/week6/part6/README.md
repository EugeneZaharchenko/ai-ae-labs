# Week 6 · part 6 — the course harness as a kagent agent (final demo)

The runbook for the Day 12 closing demo: the same ADK Go harness the course built, served over A2A,
deployed into kagent as a BYO agent, then into an Agent Substrate sandbox where egress is denied by default.

Versions, verified 2026-10-21 (re-check on the day — kagent ships weekly):
kagent **v0.10.1** (08.09.2026, pre-1.0, CNCF Sandbox) · ADK Go **v2.6.0** · a2a-go **v2.6.0** · Go **1.27**.
kagent v0.10.1 pins a2a-go v0.3.15 alongside v2.3.1, so the harness speaks the **A2A v0.3 wire by default**
(`A2A_WIRE=0.3`); kagent `main` has moved to a2a-go v2.6.0 — set `A2A_WIRE=1.0` there. The agent card is
served in a union of both formats, at both `/.well-known/agent-card.json` and `/.well-known/agent.json`.

## What the harness does

| Control | How | What the demo shows |
|---|---|---|
| Surface | `BeforeToolCallback` allowlist | `delete_ledger` is visible to the model but refused, with the reason |
| Egress probe | `check_egress` makes a real HTTPS GET | reachable on a laptop; **blocked** inside the sandbox |
| Honesty | every refusal and failure is a tool result with a cause | nothing fails silently |

`-offline` swaps the model for a rule-based stand-in (`offline-stand-in (NOT an LLM)`) so the demo runs with no key.

## 0 · Local, no cluster, no key (the early win)

```bash
# from the examples module root
go test ./week6/part6/...                       # both A2A wires, surface guard, egress both ways
go run ./week6/part6/cmd -offline &             # serves :8080 in the background
curl -s localhost:8080/.well-known/agent-card.json | head -c 300
kill %1
```

## 1 · Image

```bash
docker build -f week6/part6/deploy/Dockerfile -t REGISTRY/refund-harness:0.1.0 .
docker push REGISTRY/refund-harness:0.1.0
```

Static binary on `FROM scratch`, non-root, `PORT=8080` — the Week 2 service image shape. CA roots are copied in,
otherwise every egress probe would fail for the wrong reason and the sandbox step would prove nothing.

## 2 · kagent + the agent

```bash
kagent install --profile demo                                   # kagent quick start
sed -i 's#REGISTRY#<your-registry>#' week6/part6/deploy/*.yaml
kubectl apply -f week6/part6/deploy/agent-byo.yaml
kubectl port-forward svc/kagent-controller 8083:8083 -n kagent
curl localhost:8083/api/a2a/kagent/refund-harness/.well-known/agent.json
```

## 3 · Task → trace

```bash
kagent invoke --agent refund-harness --task "May we refund 120 EUR on order 8841?"
```

Or `kagent dashboard` and chat. Expect `refund_policy → {"allowed":true,...}`.

## 4 · Deliberate break: leave the surface

```bash
kagent invoke --agent refund-harness --task "Delete the ledger entry for order 8841"
```

Expect `{"refused":true,"reason":"tool \"delete_ledger\" is outside this agent's allowed surface"}`.

## 5 · Proof of isolation

```bash
kagent invoke --agent refund-harness --task "check egress to https://example.com"   # reachable:true
kubectl apply -f week6/part6/deploy/sandboxagent-byo.yaml                           # needs Agent Substrate
kagent invoke --agent refund-harness-sandboxed --task "check egress to https://example.com"  # reachable:false
```

Then put `example.com` into `spec.sandbox.network.allowedDomains`, re-apply, ask again: reachable. One field,
observable from the chat.

**Verify on the day:** the kagent docs show `kagent invoke` and the `/api/a2a/{namespace}/{name}` route for an
`Agent`; that a `SandboxAgent` is reached the same way is inferred from the API reference, not from a doc example.
`SandboxAgent` also needs Agent Substrate (an `ate.dev` WorkerPool) — pre-production; say so on screen.

## Fallback

Record steps 2–5 the day before. If the cluster or the network fails live, play the recording and run step 0
locally — it exercises the same harness, the same A2A wire and the same refusals.

## Sources

- kagent BYO example: https://kagent.dev/docs/kagent/examples/a2a-byo
- kagent API reference (`SandboxAgent`, `SandboxConfig`, `NetworkConfig`, `AgentHarness`): https://kagent.dev/docs/kagent/resources/api-ref
- kagent quick start: https://kagent.dev/docs/kagent/getting-started/quickstart
