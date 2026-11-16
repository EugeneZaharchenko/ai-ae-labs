# ДЗ 12 — Personal Research Agent із безпечним самополіпшенням (Eugene Zakharchenko)

Фінальний капстоун курсу. Агент відповідає на дослідницькі питання про внутрішні політики
LEDGERWORKS і **покращує власну стратегію пошуку** за принципами `karpathy/autoresearch` — але в
межах п'яти гальм, які видно в коді, у тестах і в `experiments.log`.

> Чесне застереження з лекції: autoresearch — research toy, а не enterprise-патерн «як є». Це
> research-grade прототип, який доводить, що патерн працює. Чого бракує до продакшну — окремий розділ нижче.

**Версії (станом на 16.11.2026):** Go 1.27.1 · `google.golang.org/adk/v2` **v2.5.0** (так закріплено в
`go.mod`; у тексті ДЗ і стартері згадано v2.6.0 — я не міняв пін) · модель `gemini-3.8-flash`
(з `apps/.env` через `internal/modelcfg`) · eval-набір створено **16.11.2026** (`eval/*.json`, поле `created`).

## Що зроблено (коротко)

```
START → plan ─┬→ kb_search ────┐
              └→ graph_lookup ─┴→ gather (JoinNode) → collect → draft_report
      → critic_loop (динамічний вузол) → approval_gate (HITL) → final_report
```

| Крок | Звідки | Файл / вузол |
|---|---|---|
| план (координатор): розбиття питання на підзапити | ДЗ 10 | `pipeline.go` → `plan`, `decompose` |
| паралельний збір: BM25 + rerank по базі знань і граф сутностей, fan-out + `JoinNode` | ДЗ 5–6, ДЗ 10 | `retrieval.go`, `pipeline.go` → `kb_search`, `graph_lookup`, `gather` |
| чернетка звіту з цитатами `[DOC-ID]` | — | `writer.go` → `draft_report` |
| цикл критиків: об'єктивний `citations` + LLM `completeness`, `clarity` у порядку зі стратегії | ДЗ 11 | `critics.go` → `critic_loop` |
| HITL-затвердження, рішення — у журналі, а не у відповіді | ДЗ 10 | `gate.go` → `approval_gate`, `approvals.log` |
| самополіпшення `strategy.md` | solution/ (адаптовано) | `improve.go`, `proposer.go`, `scorer.go`, `audit.go` |
| ++ Advanced: паралельні воркери в ізольованих git worktree | — | `parallel.go` |

Кожен вузол пише рядок в event log (`StateDelta`, ключ `w6p2:<вузол>`), кожен експеримент —
JSON-рядок у `experiments.log`, кожне рішення людини — у `approvals.log`.

**Підсумок метрики:** dev **0.381 → 0.952**, нові питання (generalization) **0.286 → 1.000**.

## Як відтворити один повний ран самополіпшення

```bash
git clone https://github.com/EugeneZaharchenko/ai-ae-labs && cd ai-ae-labs && git switch hw12
cp apps/.env-example apps/.env              # і вписати GEMINI_API_KEY=...
cd week6/Day12_Self_Improving_Agents_Demo_Day/labs12
go run . --self-improve --eval=eval/ --strategy=./strategy.md --experiments=6
go run . --replay                           # перевірити цілісність experiments.log
go run . --evaluate --set=generalization    # чинна стратегія на нових питаннях
go run . --ask "Payouts are down. Which severity is this?"   # конвеєр із паузою для людини
go test ./...                               # без ключа і без мережі
```

У ДЗ команда записана як `go run ./cmd/research --self-improve …` — у мене `main` лежить у самій
`labs12` (стартовий шаблон був там), тому та сама команда запускається як `go run . --self-improve …`.
Без ключа (або з `--offline`) усе працює на правилах замість моделей, і перший рядок виводу про це каже.

## П'ять принципів autoresearch — де вони в коді

### (а) Обмежена поверхня змін

- `internal/sandbox/sandbox.go` → `sandbox.IsMutable(root, path)`: `true` **лише** для `strategy.md`
  (точне ім'я) і файлів у `experiments/`. Відхиляє `..`, абсолютні шляхи, **rooted-шляхи Windows**
  (`\x`, `/x` — `filepath.IsAbs` для них каже false) і симлінки. Тест: `TestIsMutable` (21 випадок).
- Перевірка двічі й незалежно: в інструменті `write_strategy` (`proposer.go`) і ще раз у
  `Engine.check` (`improve.go`). Тест `TestKeepDiscardRefuseRevert`: `../main.go`, `eval/dev.json`,
  `experiments/x.md` як «стратегія» → `refused`, `main.go` не створено.
- Дискарди лягають у `experiments/` (наприклад `experiments/exp-20261116-223903-attempt01.md`).
- `experiments.log`, `approvals.log`, код, `eval/` — поза поверхнею: агент свою історію не редагує.

### (б) Фіксований бюджет

- `Budget` + `Accountant` у `improve.go`: стеля експериментів (`--experiments`, за замовчуванням 20),
  стеля токенів за `UsageMetadata` з проєкцією (наступний експеримент не стартує, якщо не влізе),
  час на експеримент. `MaxExperiments = 0` — помилка, а не «безмежно».
- Друге незалежне гальмо — `loopagent.MaxIterations` (`proposer.go` → `NewSelfImproveAgent`, 0 відхиляється).
- Третє — `max_attempts` у самій стратегії.
- Кожне eval-питання проходить у вузлі `eval_case` з `workflow.NodeConfig{Timeout: 60 * time.Second}`
  (`scorer.go`). Хто не встиг — 0 фактів за це питання (повільність коштує балів), а мережевий збій —
  помилка виміру (щоб збій не виглядав як «стратегія погіршилась»).
- Тести: `TestBudgetStopsExactlyAtLimit` (рівно 3 з 3), `TestStrategyMaxAttemptsIsItsOwnCap`,
  `TestTokenBudgetProjection`, `TestLoopAgentMaxIterationsHolds` (бюджет 10, loopagent 2 → 2),
  `TestEvalCaseTimeoutCountsAsZero`.

### (в) Єдина метрика

- `internal/metric/metric.go`: `score = facts_found / facts_total`, `Score` — чиста функція без LLM,
  нормалізує `2,500 EUR` / `2500 eur`, нерозривні пробіли, `**жирний**`.
- Власний eval-набір `eval/dev.json`: **6 питань, 21 очікуваний факт**, кожен із типом (`number`,
  `duration`, `date`, `name`, `quote`). Питання **багатодокументні** (вбудований у лабу набір
  насичувався до 1.0 за один крок — на ньому ≥5 ітерацій нічого б не показали).
- `eval/generalization.json`: 3 **нових** питання з новими фактами; на keep/discard не впливають.
- `TestRepoEvalSets` перевіряє, що кожен факт справді є в базі знань і що набори не перетинаються.
- `NoGoldenLeak` (`scorer.go`) відхиляє стратегію, у яку вписали відповіді: це шпаргалка, а не метод.
- keep/discard (`Decide`): keep лише якщо метрика зросла більше ніж на ε = 0.04; нічия = discard.

### (г) Стратегія — окремий людино-читний артефакт

`strategy.md` парситься й валідується (`strategy.go` → `ParseStrategy`): рівно одна метрика,
allowlist джерел (`kb_search`, `graph_lookup`) і критиків (`citations`, `completeness`, `clarity`),
межі для кожного важеля (`top_k` 1–8, `read_docs` 0–4, `max_subqueries` 1–6, `critic_rounds` 1–3),
інструкція ≤ 1200 символів, невідомі секції заборонені (місце, де можна щось сховати від рев'ю).
Стратегія справді керує поведінкою: з неї будується граф (джерела, параметри пошуку, порядок критиків,
інструкція писаря). Тест `TestStrategyChangesBehaviour`.

`git log strategy.md` показує обох авторів:

```
9934b2f Eugene Zakharchenko: strategy: human edit — citations critic first, two revision rounds, answer every part
aa4a5bf self-improve-agent: strategy: run A keeps (0.381 → 0.952, 4 keep / 2 discard)
6c83cfd Eugene Zakharchenko: Hw12: Personal Research Agent — full pipeline and bounded self-improvement core
c052f16 Dmytro Rashko: week6: Day 11 and Day 12 labs, Taskfile tasks, Day 12 solution web mode
```

(Ран B нічого не змінив у `strategy.md` — усі 4 кандидати відкинуто, — тому його коміт сюди не потрапляє.)

### (д) Повний лог

`audit.go`: `experiments.log` — append-only (`AuditLog` бере лише `io.Writer`), один JSON-рядок на
експеримент, і keep, і discard, і refused. Поля: `prev_metric`, `new_metric`, `verdict`,
`diff_summary`, `old_strategy`, `new_strategy` (повний текст + sha), `git_sha`, `duration_sec`,
`eval_tokens`, `proposer_tokens`, `discard_path`, `detail`. `go run . --replay` перевіряє цілісність
(`Replay` ловить видалений чи переставлений рядок і розірваний ланцюжок метрик; тест `TestAuditReplay`).
Реальний рядок (довгі тексти скорочено):

```json
{"id":1,"attempt":1,"verdict":"keep","reason":"метрика зросла 0.3810 → 0.6667 (+0.2857 > ε=0.0400)",
 "target_path":"strategy.md","diff_summary":"Set read_docs to 1 so the agent reads the full document already identified by the top snippet…",
 "prev_metric":0.381,"new_metric":0.667,"scored":true,"old_strategy":"# Strategy v1: one search, answer from the best snippet…",
 "new_strategy":"# Strategy v2: read full text of best document…","old_strategy_sha":"6ad50cfe158b6560",
 "new_strategy_sha":"7414904673479b2a","git_sha":"6c83cfd","duration_sec":16.67,"eval_tokens":8530,"proposer_tokens":12271}
```

Примітка: `.gitignore` репозиторію ігнорує `week6/**/experiments.log` (щоб стартер лишався чистим),
тому журнали я додав `git add -f` — капстоун вимагає їх у репозиторії.

## Прогони

Усе — жива модель `gemini-3.8-flash`, 16.11.2026. Журнал: `experiments.log` (ран A і B),
`runs/*/experiments.log` (ран C і бенчмарк).

### Ран A — самополіпшення з v1 (6 ітерацій)

| # | що запропонував агент | метрика | вердикт |
|---|---|---|---|
| 1 | `read_docs` 0→1: читати повний текст найкращого документа | 0.381 → 0.667 | keep |
| 2 | `top_k` 1→3 | 0.667 → 0.857 | keep |
| 3 | `read_docs` 1→2 | 0.857 → 0.905 | keep |
| 4 | `max_subqueries` 1→2 | 0.905 → 0.952 | keep |
| 5 | `max_subqueries` 2→3 | 0.952 → 0.952 | discard (нічия) |
| 6 | додати `graph_lookup` | 0.952 → 0.952 | discard (нічия) |

6 хв 15 с, 290 664 токени. Агент сам знайшов ті самі важелі, що знайшла б людина: читати документ
цілком, брати більше уривків, розбивати питання на частини.

### Ручна правка `strategy.md` між ранами (людина, коміт `9934b2f`)

Я додав правила, які хотів би compliance: **спочатку об'єктивний критик `citations`**, потім
`completeness`, потім `clarity`; до 2 раундів виправлень; «відповідай на кожну частину питання окремим
рядком, копіюй цифру з одиницею як у джерелі, `[DOC-ID]` одразу після неї».

Що змінилось у поведінці (видно в `detail` записів 6 і 7 журналу):

| | до (запис 6) | після (запис 7) |
|---|---|---|
| критики | `clarity` | `citations → completeness → clarity` |
| форма звіту | суцільний абзац | рядок на кожну частину питання (`- Incident severity: …`) |
| чесність про прогалини | — | «The evidence does not mention…» замість вгадування |
| eval-токени / експеримент | ≈ 10–13 тис. | ≈ 21–24 тис. (два додаткові критики) |
| метрика | 0.952 | 0.952 |

### Ран B — самополіпшення після ручної правки (4 ітерації)

4 × discard (нічия 0.952): `max_subqueries` 2→3, `top_k` 3→5, `graph_lookup`, `read_docs` 2→3.
Чесне плато — див. «Що не вийшло». Разом із раном A — **10 ітерацій** у `experiments.log`.

## Метрика якості

### Базовий (v1) vs фінальний (v6) прогін на dev

| питання | що питає | v1 | фінал |
|---|---|---|---|
| DV1 | ліміт агента, хто друге затвердження, строк повернення | 1/3 | 3/3 |
| DV2 | тариф Growth, резерв і строк, частота KYC-перегляду | 2/5 | 4/5 |
| DV3 | severity, час пейджингу, кому ескалація | 1/3 | 3/3 |
| DV4 | місяць аудиту PCI, сервіс токенізації, зберігання логів | 1/3 | 3/3 |
| DV5 | FX-маржа, час фіксації, ціна instant payout, хто звітує | 1/4 | 4/4 |
| DV6 | цитата значення коду 4837, команда, строк еквайра | 2/3 | 3/3 |
| **разом** | | **8/21 = 0.381** | **20/21 = 0.952** |

### Які типи фактів знаходяться краще

| тип | v1 | фінал |
|---|---|---|
| `number` (2.4%, 0.25 EUR, 500 EUR) | 2/6 | **6/6** |
| `duration` (5 business days, 180 days) | 1/6 | **5/6** |
| `name` (Compliance Officer, Head of Payments) | 3/6 | **6/6** |
| `date` (March, 10:00) | 1/2 | **2/2** |
| `quote` («no cardholder authorization») | 1/1 | 1/1 |

Найбільше виросли **числа й тривалості**: v1 бачив лише один уривок-«сніпет», а цифри в політиках
лежать глибше в тексті — `read_docs` і `top_k` їх дістали. Ціна: ≈1 тис. → ≈3 тис. токенів на питання.

### Generalization — 3 нових питання (не було в dev)

| питання | v1 | фінал |
|---|---|---|
| G1 зберігання KYC-файлів, поріг бенефіціара | 1/2 | 2/2 |
| G2 мінімальна виплата, звільнення з risk-hold | 1/2 | 2/2 |
| G3 SEV2: оновлення і post-mortem, перша відповідь P2 | 0/3 | 3/3 |
| **разом** | **2/7 = 0.286** | **7/7 = 1.000** |

Стратегія не overfit-нулась: вона змінила **метод** (скільки і що читати), а не вивчила відповіді
(`NoGoldenLeak` і не дав би).

## ++ Advanced — паралельні воркери самополіпшення

`parallel.go`: раунд = K кандидатів. Кожен воркер має власного агента-пропонента (з різним фокусом:
ширина пошуку / що читати / критики / вартість), пропозиції генеруються **паралельно**, кожен кандидат
пишеться у **власний `git worktree`** (`git worktree add --detach`) і читається звідти через пісочницю,
оцінка — конкурентно через `errgroup`. Перемагає найкращий score, keep — лише якщо він кращий за
поточний на ε; програвші — discard у `experiments/`. Переможця логуємо останнім, щоб ланцюжок метрик
відтворювався. Тести: `TestParallelRoundKeepsBest`, `TestGitWorktreeIsolator` (справжній worktree,
перевірка, що воркер не зачепив справжній `strategy.md`).

Порівняння з тієї самої v1, однаковий годинник від старту (бенчмарк, `runs/bench-*`):

| | послідовно (3 експ.) | паралельно ×3 (1 раунд) |
|---|---|---|
| перший keep | **t+49 с** | t+82 с |
| досягнуто 0.905 | t+96 с | **t+82 с** |
| токени до 0.905 | 83 тис. | **53 тис.** |

Чесно: **не «2–3× швидше»**. Паралельний раунд швидше й дешевше дійшов до найкращого значення, бо
одразу перевірив три напрями (переможець — `top_k` 1→4). Але перший keep прийшов пізніше: раунд чекає
найповільнішого кандидата, а 9 конвеєрів одночасно впираються в rate limit Gemini (оцінка одного
кандидата ≈18 с соло, у раунді — орієнтовно ≈60 с). Перша версія генерувала пропозиції по черзі — t+97 с; після
переходу на паралельні пропозиції стало t+82 с. Один прогін на режим — це не статистика.

## Event log — реальний живий прогін із HITL

`go run . --by=oksana --ask "Payouts are down right now. Which incident severity is this, how quickly must on-call be paged, and how often do status updates go out?"` (відповідь `approve` подана через stdin):

```
  plan                     2 підзапит(и): Payouts are down right now. Which incident severity … | Payouts are down right now; джерела: kb_search
  graph_lookup             пропущено: вимкнено стратегією
  kb_search                5 уривків з 2 документів
  collect                  5 уривків з kb_search
  draft_report             чернетка: 141 символів, 1 цитат
  critic_loop#1            citations: passed; completeness: passed; clarity: passed

── ПАУЗА approve-report-e-b3ea726e-…: потрібне рішення людини ──
This incident severity is SEV1 [INC-007].
On-call must be paged within 5 minutes [INC-007].
Status updates go out every 30 minutes [INC-007].

докази: хеш sha256:51c58fd7c7095f13; критики пройшли: true (…); цитовані джерела: [INC-007]; джерела доказів: [kb_search]
журнал: approve-report-e-b3ea726e-… → approved (by oksana)
  approval_gate            approved by oksana; критики: пройшли
  final_report             рішення approved (oksana)

══ ФІНАЛЬНИЙ ЗВІТ ══
… — затверджено: oksana; критики: пройшли (1 раунд.); sha256:51c58fd7c7095f13
токени (UsageMetadata): 3032
```

Людина бачить докази (хеш звіту, вердикти критиків, цитовані джерела), рішення спершу пишеться в
`approvals.log`, і лише потім граф відновлюється; відповідь у resume-повідомленні не вважається рішенням
(тест `TestHumanGate`: resume без запису в журналі → `invalid by system:gate`). Тиша протягом `--window`
= відмова (`timed_out`), нерозпізнана відповідь = `invalid`. У режимі eval шлюз затверджує як
`system:eval` і так і пише — eval не прикидається, що людина дивилась.

## Тести

`go test ./...` — без ключа і без мережі (офлайн-правила й `fakellm`, LLM-критики в mock-режимі).
Покриття: `labs12` 89.7 %, `internal/metric` 100 %, `internal/sandbox` 86.4 %. Окрім тестів гальм вище:
повний граф офлайн (`TestPipelineOfflineFullGraph`), критик цитат ловить вигадану цифру і письменник
виправляє її в другому раунді (`TestCriticLoopRevisesAnInventedFigure`), критик після першого провалу не
викликається — порядок має значення (`TestCriticOrderMatters`), впалий критик не схвалює
(`TestBrokenCriticDoesNotApprove`), усі режими CLI (`cli_test.go`).

## Що не вийшло / де агенту не вдалось

1. **Плато 0.952 через правило «одна зміна за раз».** Останній факт (DV2: як часто переглядають KYC
   високоризикового мерчанта — у KYC-005) потребує одночасно ширшого пошуку *і* читання третього
   документа. Агент перепробував рівно ці важелі, але по одному — кожен окремо дає нічию, і він чесно
   їх відкинув. Правило робить зміни атрибутованими, але застрягає в локальному оптимумі.
2. **`graph_lookup` не допоміг жодного разу.** Мій граф сутностей (абревіатури, назви ролей, коди) не
   пов'язує «KYC review cycle» з реченням «Review cycles by risk tier…», бо в реченні немає слова KYC.
   Для цих питань BM25 + повний текст документа виявились кращими.
3. **Паралельний режим не дав обіцяних 2–3×** (див. Advanced): упирається в rate limit провайдера.
4. **Я видалив журнал першого паралельного бенчмарку.** Коли переробив генерацію пропозицій, перезапустив
   бенчмарк у тій самій теці й стер старий `runs/bench-par` — його цифри (t+97 с) лишились лише у виводі
   консолі. Це порушує мій же принцип (д); надалі — нова тека на кожен прогін.
5. **`duration_sec` у послідовному режимі не включає час пропонента** (`loopagent` запускає його до
   таймера рушія), а в паралельному — включає. Тому для порівняння я додав спільний годинник `t+Ns`.
6. **Одна модель на все** (писар, критики, пропонент — `gemini-3.8-flash`): ризик self-preference у
   критиків. Різні моделі на ролі не налаштовував.
7. **Критики на живих прогонах майже не спрацьовували**: писар з першого разу цитував правильно, тож
   `critic_loop` лишався в 1 раунді. Що критик цитат ловить вигадану цифру — доведено тестом, не живим прогоном.
8. **Windows**: тест еталона `TestSurface_RejectsEscapes` падав (`/abs` на Windows не «абсолютний»);
   виправив `solution/surface.go` так само, як свою пісочницю.

## Що цьому агенту ще бракує до продакшну

Усі три шари — за межами курсу, але без них цей агент не можна випускати поруч із реальними грошима.

1. **Ізоляція (sandboxing — gVisor / Firecracker microVM).** Мої гальма — перевірки *всередині процесу*:
   між `sandbox.Resolve` і записом є вікно TOCTOU (інший процес може підмінити файл симлінком), а сам
   процес бачить усю файлову систему, мережу й `apps/.env`. Агент, який змінює власну поведінку, мусить
   жити в середовищі, де навіть помилка в моєму коді не дає доступу далі одного каталогу.
2. **Ідентичність (SPIFFE / SVID, автентифікація агента).** Зараз «oksana» в `approvals.log` — це просто
   рядок із прапорця `--by`, а «self-improve-agent» у `git log` — значення `git -c user.name`. Нічого з
   цього не доведено криптографічно. У продакшні кожен воркер має отримувати короткоживучу workload-
   ідентичність (SVID), людина — підписувати рішення, а коміти агента — бути підписаними його ключем,
   щоб аудитор знав, *хто насправді* змінив стратегію чи схвалив звіт.
3. **Durable execution (Temporal).** Цикл живе в пам'яті одного процесу: падіння посеред ітерації губить
   поточний експеримент (журнал зберігає лише завершені), а пауза HITL триває, лише поки живий процес і
   не минуло `--window`. Temporal дав би durable-таймери (людина може відповісти завтра), активності з
   повторами й ідемпотентністю (оцінка кандидата не повториться двічі після рестарту воркера) і
   відновлення циклу з того самого кроку.
