---
part: courses/AI_Agents_Engineering/lectures/week4/Day8_Fault_Tolerant_Tool_Harness/
lab: courses/AI_Agents_Engineering/lectures/week4/Day8_Fault_Tolerant_Tool_Harness/labs/
artifact: Fault-Tolerant Tool Harness
status: idea
adk: google.golang.org/adk/v2 v2.6.0 (модуль лаб: Go 1.27.1; сам ADK вимагає ≥1.26.6) — збірка й тести на v2.6.0, джерельні твердження звірено diff-ом v2.5.0→v2.6.0, 2026-10-14
research: research/course-weeks/week4-part2-fault-tolerant-tool-harness.md
---

# Idea: Fault-Tolerant Tool Harness

## Problem

Tools fail. The API 500s, the network hangs, the model passes a nonsense
argument. Tutorials treat all three the same way — retry — and that is how one
bad row becomes five real ones.

## Learner outcome

Classifies a failure before responding to it: transient failures get bounded
retries from the runtime; semantic failures go back to the model as a structured
observation it can act on. Knows why a write must be idempotent before it is
retried.

## Artifact

A harness with classified errors, bounded self-correction, deterministic failure
fixtures, and recovery metrics.

## Must cover (STEARING — hard contract)

Failure as normal · **failure levels kept separate** (runtime retry for
transient vs error-as-Observation for semantic) · Go sentinel/wrapped errors,
`errors.Is` / `errors.As` · structured feedback for self-correction, never blind
retry · bounded retries with observable logs · **idempotency, cancellation,
timeout, side-effect ordering** · deterministic failure fixtures on strict mocks
· observability (event log, structured logs, recovery rate, retries-to-success,
time-to-diagnosis) · output vs trajectory eval · the artifact.

## Verified constraints (2026-07-29)

- Retry defaults in the current materials are wrong three ways: **100 ms vs 1 s**
  base delay; **jitter is on by default** (deferred to "Course 3" in the text);
  and the `MaxAttempts` retry-count is off by one.
- The emitted OTel span is named **`generate_content`**, not `chat`. The slide
  contradicts the trace the learner is looking at in the same lab.
- **ADK already ships `retryandreflect`** in `plugin/`. The lecture hand-builds
  it for 20 minutes without mentioning it. A senior greps `plugin/` during the
  lab and finds it — a §2b senior-engagement failure.
- OTel GenAI semantic conventions are **`Status: Development`** and the repos
  have relocated. The lecture's caution is right; the URLs and stability wording
  are stale.
- No ADK Go trajectory-eval API exists in `google.golang.org/adk/v2`. The
  lecture correctly declines to promise one — **keep it that way.**

## Known defects to avoid — the important one

**Retry is currently taught as free.** The running example `open_refund_case` is a
**write**, the default policy retries it up to 5×, and idempotency appears
nowhere in the part. The lecture's own fix could turn one garbage row into five
duplicate real ones.

This is flagged in the research as **the STEARING item most at risk of being
silently dropped — item 6 cannot be ticked today.** The lab is where it gets
fixed: an idempotency key on every write tool, and a test that proves five
retries produce one row.

Two more overstatements to correct:

- **Self-correction is presented as reliable.** ReflecTool-Bench puts correction
  accuracy on a model's *own* tool-use errors below 20–30%. The Block 5 "75%
  recovery" story only holds because transient recovery carries it.
- **`recovery rate ≥ 0.85`** is presented in three files as a production
  readiness threshold; it is a course convention. And a floor-only assertion
  cannot catch the silent retry-loop regression — a run that retries 50 times
  and eventually succeeds scores 1.0.

## Test strategy

- Deterministic fault fixtures via `httptest`: 500, timeout, connection reset,
  malformed body, and a semantically-wrong-argument case.
- **Idempotency test is the centrepiece:** same idempotency key, five retry
  attempts, exactly one effect. Assert on the effect, not on the call count.
- `errors.Is` / `errors.As` classification table test — every fixture maps to
  exactly one class.
- Cancellation mid-retry: no further attempt after context cancellation.
- Metrics: assert retries-to-success and that a pathological retry loop trips a
  **ceiling**, not just clears a floor.

## Out of scope

The ReAct loop itself (Part 1) · durable execution / Temporal (later course) ·
production OTel backend wiring · trajectory-eval framework.

## Open questions for the spec

1. Idempotency: is it a lab-implemented key, or does the harness delegate to the
   tool? **This decides whether STEARING item 6 can be ticked.**
2. Do we teach `retryandreflect` first and have the learner extend it, or
   hand-build then reveal it? (Reveal-after is a legitimate teaching device — but
   only if the lecture says so out loud.)
3. What replaces `recovery rate ≥ 0.85` as an honest readiness signal — a
   ceiling on retries-to-success alongside the floor?
4. Which OTel semconv revision do we pin, given `Status: Development`?
