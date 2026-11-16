# Strategy v5: allow two subqueries for multi-part questions

## Metric
- facts_found_ratio

## Sources
- kb_search

## Search
- max_subqueries = 2
- top_k = 3
- read_docs = 2

## Critics
- clarity

## Instruction
Answer in one or two short sentences.

## Stop conditions
- max_attempts = 20
- critic_rounds = 1
