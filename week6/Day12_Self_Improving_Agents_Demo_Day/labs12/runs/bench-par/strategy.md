# Strategy v2: retrieve top 4 passages

## Metric
- facts_found_ratio

## Sources
- kb_search

## Search
- max_subqueries = 1
- top_k = 4
- read_docs = 0

## Critics
- clarity

## Instruction
Answer in one or two short sentences.

## Stop conditions
- max_attempts = 20
- critic_rounds = 1
