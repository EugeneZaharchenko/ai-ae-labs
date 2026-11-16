# Strategy v6 (human edit): objective citation check first, answer every part

## Metric
- facts_found_ratio

## Sources
- kb_search

## Search
- max_subqueries = 2
- top_k = 3
- read_docs = 2

## Critics
- citations
- completeness
- clarity

## Instruction
Answer every part of the question, one short line per part, in the order the question asks.
Copy each figure with its unit exactly as the source writes it, and put the [DOC-ID] right after it.
If the evidence does not cover a part, say so on that line instead of guessing.

## Stop conditions
- max_attempts = 20
- critic_rounds = 2
