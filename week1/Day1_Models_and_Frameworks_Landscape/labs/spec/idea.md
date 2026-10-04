---
part: week1/Day1_Models_and_Frameworks_Landscape/
lab: week1/Day1_Models_and_Frameworks_Landscape/labs/
artifact: Cross-Model Benchmark Harness
status: idea
adk: google.golang.org/adk/v2 v2.5.0 (модуль лаб: Go 1.27; сам ADK вимагає ≥1.26.6) — звірено 2026-10-01
research: research/course-weeks/week1-part1-models-and-frameworks-landscape.md
---

# Idea: Cross-Model Benchmark Harness

## Problem

A learner picks a model the way they pick a phone — by reading a leaderboard.
Then the bill arrives, the p95 latency misses the SLA, or the data-residency
review kills the project. Model choice is an engineering decision with at least
six axes, and none of them is "who is #1 this week".

## Learner outcome

Runs one prompt across several providers and produces a **measured** comparison
— tokens in/out, wall-clock latency, cost — rather than an opinion. Can defend a
model choice with numbers from their own machine.

## Artifact

A Go CLI that sends an identical task to N configured providers, records
per-provider metrics, and emits a comparison table. It must be honest about
variance: one sample is not a benchmark, so report n and spread, not a single
number.

## Must cover (STEARING — hard contract)

Model choice as a decision framework · token economics (effective cost, not
price sheets) · context window as bounded working memory · dated model snapshot
with verify-before-recording · framework framing (ADK Go default, LangGraph for
self-evolving graphs, DeepAgents comparison) · reasoning effort as a knob ·
measurable harness, not a ranking · LEDGERWORKS business risks · Part 2 bridge ·
completions vs streaming, provider dialects, Go providers (Ollama, Gemini,
OpenAI).

Proposed items to honour: LLM vs agent · RAG bridge · MCP bridge · lab
foundation map.

## Verified constraints (2026-07-29)

- ADK Go v2.2.0 ships **three** model packages: `model/gemini`,
  `model/openaimodel`, `model/apigee`. **No Anthropic backend.**
- Ollama is reachable via `openaimodel.ClientConfig.BaseURL` ("for
  OpenAI-compatible endpoints"). Working precedent:
  `labs/.../week1/Part2_.../agent.go:BuildModel`.
- Token usage lives on `model.LLMResponse.UsageMetadata`, type
  `*genai.GenerateContentResponseUsageMetadata` → `.PromptTokenCount`,
  `.CandidatesTokenCount`, `.ThoughtsTokenCount`. There is no
  `usage.InputTokens`.
- `model.Register` / `model.NewLLM` (new in v2.2.0) give name-pattern provider
  resolution; registration is opt-in and ambiguous matches are an error.

## Known defects to avoid

The current materials carry 13 contradictions against primary sources — see the
research doc. The three that would break on camera: Opus 4.8 context stated as
200K (it is 1M, and `Top_models.md` already says so — two course files
disagree); `Gemini 3.5 Pro` does not exist; Grok 4.3/256K is wrong twice.

**Design implication for this lab:** never hardcode a model list. Read it from
config, and make an out-of-date entry fail loudly with the provider's own error
rather than silently benchmarking a model that was renamed.

## Test strategy

- `httptest` fake provider endpoints — assert metric extraction, not model quality.
- Cost arithmetic is pure and table-tested, including zero-token and
  missing-usage responses (some providers omit usage on streamed responses).
- No live API call in `go test ./...`. Live runs are an explicit flag.

## Out of scope

Publishing a ranking · quality/accuracy scoring (that is week 6 eval literacy) ·
fine-tuning · anything requiring an Anthropic backend.

## Open questions for the spec

1. How many samples per provider before the numbers mean anything, and do we
   report median or mean?
2. Streaming and non-streaming have different latency semantics — do we measure
   TTFT, total, or both?
3. Where does the price table live so it can be refreshed without a code change,
   and how does it carry its «станом на» date?
4. Does the harness fail or degrade when one provider is unconfigured?
