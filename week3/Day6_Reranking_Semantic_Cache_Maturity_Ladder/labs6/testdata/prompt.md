# Lab 6 — demo prompts for the ADK web UI

The starter graph has no LLM: each message goes straight to `search → rerank → answer`
as the query. These prompts show what the baseline gets right and where it breaks.
Each one maps to a Homework item.

## Launch

From the `labs6` folder:

```bash
go run . web api webui      # open http://localhost:8080/ui/ → agent "high_precision_rag"
```

## Prompts, in order

| # | Prompt | What it shows | Expected result (baseline) |
|---|---|---|---|
| 1 | `Що таке тариф T-2?` | First result in under 15 minutes | `[cache_hit=false]` with the `c-tariff-T2` chunk |
| 2 | `що таке тариф t-2` | Exact cache hit after the query is normalised | `[cache_hit=true]` |
| 3 | `Яка ціна тарифу T-2?` | A reworded question misses the cache → Homework item 4 (semantic cache) | `cache_hit=false` |
| 4 | `Яка ставка комісії на тарифі T-2?` | The keyword baseline mixes up T-1 and T-2 → Homework item 3 (rerank). Open the `search` node's event to see the T-1 chunk among the candidates | `c-A114-rate` (a merchant on T-2), not the T-2 definition |
| 5 | `Які мерчанти на тарифі T-2 підпадають під вимогу НБУ 2026 і хто підписував їхні договори?` | Oksana's multi-hop query. The baseline returns one chunk, so you get half the answer → Homework items 1 and 2 (fan-out, JoinNode) | One chunk, no signers |
| 6 | `Хто підписав договір CT-2025-031?` | A single-hop query the baseline gets wrong: the contract number also appears in the merchant chunk, which wins the word-overlap count → a good "before" row for rerank (item 3) | `c-A207-rate`, not the signer chunk |
| 7 | `Яка погода в Києві?` | Out-of-domain. There is no quality gate, so the baseline returns an unrelated chunk where it should say "not enough evidence" (item 1, terminal node) | Unrelated chunk |

For a single live-demo prompt, use **#5**. It is the question the homework is built around,
and the web UI's event view shows the linear graph returning only half the answer.

The expected results were checked on 08.10.2026 through `/api/run` against
`task week3:day6:web:ollama`. The starter's graph does not call either model yet, so
`task week3:day6:web` (pure Go) gives the same answers.

The answer cache lives in the server process, not in the session: a question asked in one session
is a `cache_hit=true` in every other session and for every other user until the server restarts.
Restart the server before a demo, and use it as the example for Homework item 4 (tenant isolation).
