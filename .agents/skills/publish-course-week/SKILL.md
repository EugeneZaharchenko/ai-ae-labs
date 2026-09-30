---
name: publish-course-week
description: Publish one week of the AI Agents Engineering course to the public learner repository (github.com/dimetron/ai-ae-labs) — assemble the shared repository parts plus week1..weekN and the demos that have opened, generate the week-appropriate README/AGENTS/demo index, and commit on that week's branch. Use when the user says "publish week N", "open the next week", "sync the public repo for week N", "release the week's materials", or asks why the public repository does not show a week. Covers the Monday schedule gate, what must never be published (lectures, slides, labs/spec, labs/solution, apps/.env), the per-day allowlist, and the student-facing Taskfile.
---

# Publishing a course week

This skill publishes one week of the course into a checkout of the **public
learner repository**, `github.com/dimetron/ai-ae-labs`. Learners clone it, do
their homework in their own copy, and submit a link to it, so whatever lands
there is public and permanent.

The tool is `scripts/publish-week` in the course repository. It is the only
thing that should write to the published branch: the branch is rebuilt on every
publish, and the documents there are generated from the week being published.

## Golden rules

- **One week at a time, on its Monday.** Week N opens on the Monday of
  `21.09.2026 + 7·(N−1)`, the webinar day. Publishing early hands learners
  material the course has not taught yet, which is the failure the week-by-week
  release exists to prevent. The gate refuses early runs; `-force` overrides it
  and should only be used knowingly.
- **Publish additively: keep every earlier week exactly as the target has it.**
  Week 1 is generated whole, because it is the root of the public history and
  there is nothing before it. From week 2 on the target is the base and the
  publish **only adds** the new week, its demos and its task section — shared
  trees are merged rather than wiped, and earlier weeks are not re-derived from
  this checkout. This is not a nicety, it is the reason the tool is safe to run
  repeatedly: the published repository is the record of what learners already
  cloned and legitimately carries content the monorepo does not — skills
  authored straight on the public branch (`go-test-summary`), hand-published
  mentor solutions, and week 1's own file layout from before the monorepo
  reorganized `Homework.md` into `labs/`. A rebuild would delete all of it.
  If a shipped week genuinely needs correcting, that is a separate, deliberate
  edit to the published branch — never a side effect of opening the next week.
  A dry run states this in one line (`additive: week2 is added to the target;
  weeks 1–1 stay as the target has them`), and the check is `git diff
  --name-status <main> <week-branch> -- week1` returning **empty**.
- **Never publish the taught material.** `Lecture.md` and `slides.html` are how
  the course is delivered on the platform; the course materials contract says
  explicitly that `Lecture.md` does not go to the learner repository. The tool
  enforces this with a per-day **allowlist** (`Homework.md`, `labs`, `guides`),
  not a denylist. If you add an artifact to a day directory and expect it to
  ship, add it to `dayEntries` in `scripts/publish-week/assemble.go`.
- **Never publish a mentor solution or author notes.** `labs/solution` (the
  mentor's reference, released by hand after a deadline) and `labs/spec` (the
  author's notes) stay in the course repository. The published branch's *own*
  hand-published `labs/solution` and `guides` are never deleted by a republish —
  see `publishedUnmanaged`.
- **Never publish `.env`.** `apps/.env-example` ships; `apps/.env` does not.
  Tree copies skip dotfiles for exactly this reason. `gitleaks` guards commits
  on both sides.
- **Review before pushing.** A publish commits locally and stops. Pushing
  rewrites the public `main`, so it is a separate, explicit decision.

## The command

Run from the course repository root.

```bash
task publish WEEK=3 TARGET=~/p6s/ai-ae-labs-published -- -dry-run   # show the plan
task publish WEEK=3 TARGET=~/p6s/ai-ae-labs-published               # assemble + commit
task publish WEEK=3 TARGET=~/p6s/ai-ae-labs-published -- -push      # also push to main
```

Equivalent without `task`:

```bash
go run ./scripts/publish-week 3 -target ~/p6s/ai-ae-labs-published -dry-run
```

Flags: `-src` (repository root to read from, default `.`), `-target` (the public
checkout — required), `-remote` (default `github`), `-dry-run`, `-push`,
`-force`, `-now YYYY-MM-DD` (test the schedule gate).

## What a publish writes

Week 1 is generated whole; every later week is **added** to the target. The
treatment column below is week 1's; on an additive run the shared trees are
merged instead, and only the published week and its demos are derived from the
source.

| Part | Treatment | Why |
|---|---|---|
| `go.mod`, `go.sum` | copied | the module path stays `ai-eng-course/labs`, so lab instructions and imports work unchanged |
| `internal/` | wiped + copied (week 1); **merged** afterwards | shared helper packages (`adkenv`, `fakellm`, `labrun`, and `modelcfg` from week 2) |
| `scripts/covgate.sh` | wiped + copied (week 1); **merged** afterwards | the coverage gate a learner runs on their own work |
| `Taskfile.yml` | **transformed**, never replaced | week 1 is generated from the monorepo, with `check` losing the coverage gate and the authoring tasks dropped; from week 2 on, the target file is the base and only the new week's section is appended, together with any `vars:` it needs |
| `.devcontainer/`, `.agents/`, `.githooks/`, `.gitleaks.toml`, `.gitignore`, `apps/.env-example` | copied; trees merged afterwards | the environment, the agent skills, the secret-scanning hook |
| the published week's days | wiped + copied, **allowlisted** | only the week being published; each day publishes `Homework.md`, `labs/`, `guides/` and nothing else. Earlier weeks are untouched |
| `demo/` demos opened **in this run** | wiped + copied (week 1); **merged** afterwards | a demo an earlier week already published stays as the target has it |
| a demo's `.env.example` | copied as an explicit file | `copyTree` skips dotfiles, so the README's `cp .env.example .env` would otherwise fail |
| `README.md`, `AGENTS.md`, `demo/README.md` | **generated** | they name this week's material; templates live in `scripts/publish-week/templates/` |

The published repository is **cumulative**: week 3 carries weeks 1, 2 and 3. Each
week's branch is built on the previous week's tip, so a learner who cloned once
keeps pulling. Week 1 is the root of the public history.

Because each week is built on the previous week's tip, the branch you publish is
a fast-forward of the target's `main` — unless a publish was skipped. Publish the
earlier week first rather than reaching for `-force`.

## The schedule gate

Week N opens on `21.09.2026 + 7·(N−1)` (Monday), and the two days within a week
open Monday and Thursday. The gate compares calendar **dates**, not instants, so
publishing on the correct day works at any hour — before or after the 18:00 UTC
webinar.

```text
week1  Пн 21.09.2026     week4  Пн 12.10.2026
week2  Пн 28.09.2026     week5  Пн 19.10.2026
week3  Пн 05.10.2026     week6  Пн 26.10.2026
```

A refusal looks like this, and is the normal answer to an early request:

```text
publish-week: week 3 opens Пн 05.10.2026 (webinar 18:00 UTC) — today is 21.09.2026; pass -force to publish early
```

## Demos

Which demos open with which week is a decision, kept in `demoForWeek` in
`scripts/publish-week/plan.go`. It is deliberately a small table rather than
inferred from directory names: the names carry no week number, and
`adk-quickstart` is published under a different label (`adk-quickstart-sso`).

```go
var demoForWeek = map[int][]string{
	1: {"1_ai-gateway", "adk-quickstart"},
	4: {"7_adk-go-evals"},
	6: {"7_adk-go-evals"},
}
```

Weeks 2, 3 and 5 ship **no** demo yet; their candidates are recorded in
`withheldDemo` so the intent survives, and the generated `demo/README.md` tells
learners those projects are still to come. To publish one, move it from
`withheldDemo` into `demoForWeek` **after** it builds and has a README a learner
can follow. Add its `Covers` and `Summary` to the `demos` table in the same edit.

## The student-facing Taskfile

The published `Taskfile.yml` is the course one with three changes, all enforced
by `scripts/publish-week/taskfile.go`:

1. **`check` does not run `cover`.** The starter material ships packages below
   the 85% threshold and packages with no tests, so the full gate is red on
   arrival and a learner's first `task check` would fail on code they did not
   write. `task cover` remains for their own work; `COVER_MIN` is never lowered.
2. **Authoring tasks are dropped**: `publish` (names the tool that is not
   published) plus `security` and `cover:report` — `govulncheck` is not in the
   dev container and exits non-zero on these dependencies, and a repo-wide
   coverage number is actively misleading on starter material.
3. **A `week<N>:build`, `week<N>:test`, `week<N>:run` triplet is appended** for
   every published week, so a learner can check their own week without waiting
   on six weeks of unrelated packages.

```bash
task week2:test
task week2:run PKG=./week2/Day3_First_ADK2_Agent_Workflow_Graph/labs
```

Each of these transforms is **strict**: if it cannot find what it expects in
`Taskfile.yml`, the publish fails with a line number rather than shipping a
silently wrong file. If you restructure the Taskfile, update the corresponding
list in `taskfile.go`.

## Procedure

1. **Confirm the week.** Check today's date against the schedule table above. If
   the week is not due, say so and stop — do not reach for `-force` on your own.
2. **Confirm the source is current.** This tool reads the course repository as
   it stands. If the week's materials were just synced from the authoring
   monorepo, verify that finished first; a publish of half-synced material is
   worse than no publish.
3. **Dry run and read the plan.** `-dry-run` prints every path that will be
   replaced, copied, cleared or generated. Check the week count, the day
   directories and the demos.

   ```bash
   task publish WEEK=3 TARGET=~/p6s/ai-ae-labs-published -- -dry-run
   ```

4. **Publish.**

   ```bash
   task publish WEEK=3 TARGET=~/p6s/ai-ae-labs-published
   ```

5. **Verify in the target** before any push:

   ```bash
   cd ~/p6s/ai-ae-labs-published
   ls week3/                                    # the new days, and only them
   ls week1/Day1_*/                             # Homework.md, guides, labs — no Lecture.md
   git diff --name-status origin/main public-week3 -- week1   # EMPTY — earlier weeks untouched
   git diff --name-status origin/main public-week3 | grep '^D' # EMPTY — nothing deleted
   task check                                   # green
   task week3:test                              # green
   task week3:run PKG=./week3/Day5_.../labs5    # a task the appended section defines
   gofmt -l . | head                            # empty
   git status --porcelain | head                # empty
   ```

   The two `git diff` lines are the additive check. An empty first one means the
   new week did not disturb week 1; an empty second means it deleted nothing at
   all. A non-empty deletion list is always a defect — the target carries
   content this checkout does not, and the publish had no business removing it.

6. **Push, if asked.** The push refuses a non-fast-forward unless `-force` is
   given, which is the guard against accidentally publishing a week 3 that was
   built on nothing instead of on week 2.

   ```bash
   task publish WEEK=3 TARGET=~/p6s/ai-ae-labs-published -- -push
   ```

   **Week 1 is the one exception.** It is an orphan branch — the root of the
   public history, with no monorepo commits in it — so against an existing
   public `main` it is never a fast-forward and the first publish of a fresh
   history needs an explicit `-force`. Every later week is built on the previous
   week's tip and fast-forwards normally. If a push for week 1 or later is
   refused as a non-fast-forward **after** the history exists, that is the guard
   working: something is wrong with the base, and `-force` would rewrite what
   learners already have.

## Failure modes seen in practice

These all happened while this tool was built. Each has a test; if you hit one,
read the test before changing the code.

| Symptom | Cause | Fix |
|---|---|---|
| `labs/spec` or `labs/solution` appears in the published tree | the skip is matched by suffix, but the copied tree root moved | skip lists are matched by suffix in `copyTree`; keep them that way |
| `Lecture.md`/`slides.html` published | the day directory was copied whole | the per-day allowlist (`dayEntries`); the manifest emits one tree **per day**, not per week |
| a hand-published mentor solution disappears | the wipe ignored `publishedUnmanaged` | `wipeDir` takes the unmanaged list; `assemble` must pass it (`TestAssemblePreservesPublishedSolution`) |
| publishing week 3 deletes something from week 1, or `git diff main week3 -- week1` is not empty | the manifest rebuilt every shipped week instead of only the new one | only `w.N == cp.Published` enters the manifest, and shared trees use the `merge` kind (`TestAssembleAdditivePreservesTheTarget`) |
| a skill authored on the public branch vanishes after a publish | a shared tree was wiped instead of merged | `treeKind(additive)` returns `merge` from week 2 on; `additive` comes from `week > 1` in `main.go` |
| every week task is defined twice in the published `Taskfile.yml` | the monorepo gained an inline `# --- weekN ---` section while the tool still appended one | from week 2 on the target Taskfile is the base and only a missing section is appended (`appendWeekTasks`); a present week is a no-op |
| `appendWeekTasks: no definition in Taskfile.yml for DAY3` | a week section references a var the monorepo does not define at top level | add it to the monorepo's `vars:` block, or to `taskScopedVars` if the task declares it itself |
| `task check` red on a fresh clone | `cover` was left in `check` | `dropCoverFromCheck`, which fails loudly if the Taskfile moved |
| `publish-week: no "publish" task to remove` | the Taskfile was restructured | update `authoringOnly`/`studentDrops` in `taskfile.go` |
| mojibake in the generated README | a multi-byte rune was split by byte slicing | case-fold with `[]rune`, never `s[:1]` |
| `-target` silently ignored | Go's `flag` stops at the first positional | `reorder` moves flags before the week argument |
| push refused: "not a fast-forward" | the week was built on the wrong base | publish the previous week first; `-force` only if rewriting public history is intended |

## Checks the tool must keep passing

```bash
go test ./scripts/publish-week/          # the invariants
sh ./scripts/covgate.sh 85 scripts/      # the package's own coverage gate
```

The package holds the publishing rules in code, so a rule that is only in this
document is one refactor away from being lost. When you change a rule, change it
in the tool and assert it in `publish_test.go` — the leak tests are written to
fail when the corresponding guard is removed.
