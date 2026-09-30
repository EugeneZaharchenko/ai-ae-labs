---
name: go-test-summary
description: "Use after any go test run — report the result as a box-drawing table with one row per package plus PASS/FAIL/SKIP totals, instead of prose or a raw log dump. Triggers: run the tests, run go test, run day1/day2 tests, test this package, task test / task check / task week1:day1:test finished, a package failed, or results must be reported to the user. Also use when adding a test task to the Taskfile — run it and show this table as the proof. Covers the exact table shape, the commands that produce the numbers, how to count top-level tests and not subtests, and what to report when a package fails to build or has no test files."
---

# Go test result table

A `go test` run always ends with the same table. The reader reads the table, not the
log. Do not answer a test run with prose alone. Do not paste the raw log and stop
there.

## The table

Draw the table with box characters. Do not use a markdown table. A markdown table
arrives as raw pipes and dashes in a text terminal, and the reader has to rebuild
the grid in their head.

```text
┌──────────────────────┬──────────┬───────────────────────────┐
│        Package       │  Status  │         Duration          │
├──────────────────────┼──────────┼───────────────────────────┤
│ labs                 │ ✅ ok    │ 2.667s / 1.780s (verbose) │
├──────────────────────┼──────────┼───────────────────────────┤
│ labs/cmd/mcp         │ ✅ ok    │ 2.192s / 1.238s (verbose) │
├──────────────────────┼──────────┼───────────────────────────┤
│ tests PASS           │ ✅ 60    │ —                         │
├──────────────────────┼──────────┼───────────────────────────┤
│ FAIL / SKIP          │ ✅ 0 / 0 │ —                         │
└──────────────────────┴──────────┴───────────────────────────┘
```

Rules for the shape:

- One row per package. Put a separator row between every pair of rows.
- Pad every cell so the vertical bars line up down the whole grid. Count display
  width, not bytes. An emoji occupies two columns.
- Shorten the package name to the part that identifies it (`labs`, `labs/cmd/mcp`).
  Keep the full import path in a note below the table when it matters.
- Duration column. Use the `ok` line's duration. When you ran the package twice
  (plain and `-v`), show both, as in the example. With one run, show one number.
- Aggregate rows go last: total top-level tests that passed, then FAIL and SKIP
  counts. The aggregate row is green when the count is zero for failures.
- Add a `Before` column only when you changed something and you know both states.
  Do not invent a before state.

Status values:

| Output of `go test`             | Status      |
|---|---|
| `ok  <pkg>  <time>`             | `✅ ok`     |
| `ok  <pkg>  (cached)`           | `✅ ok`     |
| `FAIL  <pkg>  <time>`           | `❌ FAIL`   |
| build error, panic, timeout     | `❌ FAIL`   |
| `?  <pkg>  [no test files]`     | `⚠️ no tests` |

## Getting the numbers

Run the tests first. Keep the output in a file, so the counting commands read a
stable copy.

```bash
go test -race -count=1 ./week1/Day2_Structured_Output_Function_Calling/... 2>&1 | tee /tmp/go-test.out
go test -race -count=1 -v ./week1/Day2_Structured_Output_Function_Calling/... 2>&1 | tee /tmp/go-test-v.out
```

Package lines, ready for the table:

```bash
grep -E '^(ok|FAIL|\?)' /tmp/go-test.out | awk '{printf "%-20s %-6s %s\n", $2, $1, $3}'
```

Top-level test counts:

```bash
grep -oE '^--- (PASS|FAIL|SKIP)' /tmp/go-test-v.out | sort | uniq -c
```

The pattern starts at column one on purpose. Go indents a subtest line by four
spaces (`    --- PASS: TestFoo/case`). A leading `^---` therefore counts test
functions and ignores their subtests. That is the number the reader wants: 60 test
functions, not 400 subtest lines.

## Report rules

- Always show the table. A run of one package still gets one, with the three
  aggregate rows.
- Show the raw `go test` command and its `ok`/`FAIL` lines below the table. The
  table is the summary. The raw lines are the evidence. Never claim a pass without
  both.
- Name every failing test below the table, one line each, with the reason. A red
  aggregate row without names is not actionable.
- Report a skip as a skip. A skipped test is not a pass.
- Say what you did not run. A package-scoped run is not `task check`. State the
  command that was not run.
- Never write `(cached)` as a fresh pass. Add `-count=1` when the run is meant as
  proof.
- Nested modules are not part of `./...` (AGENTS.md §5.5). When the target lives in
  another module, run `go test` from that module's directory and say so. Give it its
  own table, or state the module in the Package cell.
- Do not lower `COVER_MIN` or edit the Taskfile to make a red run green. Report the
  failure and its cause.

## Failure modes seen in practice

| Symptom | Cause | Fix |
|---|---|---|
| 400 tests reported for 60 test functions | the grep pattern matched indented subtest lines | anchor the pattern with `^--- ` |
| a stale pass reported | `go test` served a cached result | add `-count=1` |
| bars do not line up | cell padding counted bytes, or an emoji counted as one column | count display width; emoji take two columns |
| the table renders as pipes and dashes | a markdown table was used | use box characters |
| `no test files` counted as a pass | the `?` line was mapped to `✅ ok` | give it its own `⚠️ no tests` status |
| a passing table over a failed task | only the aggregate was read | check the exit code too, and report it |
