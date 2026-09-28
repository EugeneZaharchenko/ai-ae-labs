# Collection of AI Agentic projects

Hands-on demos accompanying the course, from platform plumbing to full agentic systems.

| # | Project | Status | What it covers |
|---|---------|--------|----------------|
| 1 | [`1_ai-gateway`](1_ai-gateway/) | config-verified | agentgateway v1.4.1 + Jaeger/Prometheus/Grafana: one OpenAI-compatible entry on `:4000` routing to a keyless mock, OpenAI, Anthropic, Gemini and Ollama Cloud, with per-request USD cost |
| 3 | [`3_adk2_patterns`](3_adk2_patterns/) | working | ADK Go v2.4.0 pattern catalog: 16 agent/workflow patterns (A1–E3) plus a no-LLM function graph and build-time guards; one folder per pattern with `main.go`, tests and README; runs offline, `-live` for a real model |
| — | [`adk-quickstart-sso`](adk-quickstart/) | working | Week 1 starter: ADK Go v2 agent + two typed tools, Google SSO via ADC (API-key fallback) |

See each subfolder's `README.md` for build and run instructions.

> Проєкти 2–7 з'являться за графіком курсу — див. графік у кореневому `README.md`.

> `adk-quickstart-sso` is deliberately unnumbered: it is the Week 1 course
> starter students download and run, not a stage in the 1→6 progression above.
> Give it a number only if it earns a slot in that sequence.
