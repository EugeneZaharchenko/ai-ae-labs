# ДЗ 8 — Fault-Tolerant Tool Harness

Агент LEDGERWORKS із ДЗ 7 тепер працює як Temporal Workflow. Кожен виклик LLM і кожен виклик інструмента — це Activity. Помилки інструментів розкладено за трьома рівнями, і в кожного рівня свій власник.

| Рівень | Збій | Хто лікує | Де в коді |
|---|---|---|---|
| L1 | транзієнтний (503, таймаут) | рушій: Temporal `RetryPolicy` | `tools.go` `toolRetry`, `sideEffectOptions` |
| L1 | семантичний (`merchant_id='undefined'`) | модель: Observation + `maxSelfCorrections = 3` | `harness.go` `budget.observe` |
| L1 | фатальний (`context.Canceled`, невідома помилка) | ніхто: хід зупиняється | `harness.go` `classifyError` → `ClassFatal` |
| L2 | `kill -9` воркера посеред tool call | durable runtime: історія подій Temporal | `workflow.go` `AgentWorkflow`, `googleadk.NewModel` |
| L3 | розмова завтра, факти після рестарту | SQLite (`.data/agent.db`) і Dgraph | `activities.go` `LoadSession`/`PersistSession`/`RememberSession` |

## Файли

- `main.go` — лише `run(os.Args)`.
- `app.go` — підкоманди `worker`, `start`, `counter`, конфігурація з env.
- `harness.go` — `classifyError`, `toActivityError`, `Observation`, `budget`, `maxSelfCorrections`, `ErrSelfCorrectionExhausted`.
- `tools.go` — інструменти `open_refund_case`, `get_merchant_payouts`, `search_nodes`, `open_nodes`; кожен викликає свою Activity з `RetryPolicy`.
- `workflow.go` — `AgentWorkflow`: раннер ADK на `googleadk.NewContext(ctx)` з `session.InMemoryService()`.
- `activities.go` — `OpenRefundCase`, `GetMerchantPayouts`, `LoadSession`, `PersistSession`, `RememberSession`, `SearchNodes`, `OpenNodes`.
- `harness_test.go`, `workflow_test.go`, `app_test.go` — офлайн-тести: без ключа, без Docker і без сервера Temporal.

## Як класифікуються помилки

`classifyError` дивиться лише на типи (`errors.Is` / `errors.As`), ніколи на текст помилки:

- `context.Canceled` або `*temporal.CanceledError` → `ClassFatal`: це сигнал зупинитись, а не причина повторювати.
- `*ledger.SemanticError` або `ledger.ErrSemantic` → `ClassSurfaceToModel`.
- `ledger.ErrTransient`, `context.DeadlineExceeded` або `*temporal.TimeoutError` → `ClassRetryTransient`.
- `*temporal.ApplicationError` → за рядком `Type()`. Go-тип помилки межу worker → workflow не проходить, проходить лише цей рядок.
- Усе інше → `ClassFatal`: невідому помилку наосліп не повторюють.

На боці воркера `toActivityError` загортає помилку ledger у `ApplicationError` з типом `SemanticError` / `TransientError` / `FatalError`. Поле з помилкою (`merchant_id`) передається в `Details`. `RetryPolicy` має `NonRetryableErrorTypes: ["SemanticError", "FatalError"]`, тож Temporal повторює лише транзієнтні збої (до 4 спроб).

У workflow `budget.observe` вирішує, що робити далі:

- семантична помилка стає Observation `{"tool", "status": "invalid_argument", "field", "observation", "attempt"}` для моделі;
- транзієнтна помилка, яка лишилась після всіх повторів, стає Observation `status: "unavailable"`;
- четверта семантична помилка за хід або фатальна помилка зупиняє хід явною помилкою workflow з типом `SelfCorrectionExhausted` / `FatalError`.

Лічильник самокорекцій живе в ході (`budget` створюється всередині workflow), а не в глобальній змінній.

Одна деталь: якщо інструмент повертає `error`, ADK не зупиняє цикл, а показує моделі `{"error": …}` і йде далі. Тому зупинка — це прапорець `budget.stop`, який workflow перевіряє після кожної події.

## Запуск

```bash
cd week4/Day8_Fault_Tolerant_Tool_Harness/labs8
docker compose --profile temporal-pg up -d --wait   # Temporal (PostgreSQL) + UI :8233 + Dgraph
# або: temporal server start-dev --db-filename .data/temporal.db && docker compose up -d dgraph
LABS8_MODEL=demo go run . worker                    # термінал 1 (без LABS8_MODEL — модель з apps/.env)
go run . start -session s1 "Open a refund for txn-2026-07-118845 at merchant A-114"   # термінал 2
go run . counter
go test ./...                                       # офлайн
```

Temporal CLI в мене не встановлений, тому всі прогони нижче — на профілі `temporal-pg` з `compose.yaml`. Для коду різниці немає: той самий `localhost:7233`.

## Доказ 1. Самокорекція (п. 2)

Офлайн-модель (`LABS8_MODEL=demo`) навмисно передає `merchant_id: "undefined"`, як в інциденті о 03:00. Ось що зробив агент:

```
$ go run . start -session s1 "Open a refund for txn-2026-07-118845 at merchant A-114"
workflow turn-s1-1793655778708 (session s1)
[ev:tool_call] open_refund_case map[merchant_id:undefined transaction_id:txn-2026-07-118845]
[ev:tool_observation ERROR] open_refund_case map[attempt:1 field:merchant_id observation:ERROR: invalid merchant_id: "undefined" was not extracted from the request — please correct your arguments and try again status:invalid_argument tool:open_refund_case]
[ev:answer] I should re-extract merchant_id from the request.
[ev:tool_call] open_refund_case map[merchant_id:A-114 transaction_id:txn-2026-07-118845]
[ev:tool_observation] open_refund_case map[case_id:rc-txn-2026-07-118845-A-114 replayed:false status:pending]
[ev:answer] Refund case rc-txn-2026-07-118845-A-114 is open for txn-2026-07-118845 at merchant A-114.
[ev:success] self_corrections=1
```

Семантична помилка не повторювалась жодного разу: `OpenRefundCase` стартував рівно 2 рази (`TestSemanticErrorBecomesObservationAndSelfCorrects`). Повторний виклик з тими самими `transaction_id + merchant_id` (сесія s4) не створив другий кейс:

```
[ev:tool_observation] open_refund_case map[case_id:rc-txn-2026-07-118845-A-114 replayed:true status:pending]
[ev:answer] Refund case rc-txn-2026-07-118845-A-114 is open … (it already existed — nothing new was created).
```

## Доказ 2. `kill -9` посеред tool call (п. 4)

Воркер з `LEDGER_LATENCY=15s`: `open_refund_case` спить 15 с, і в цей момент процес вбито.

```
$ LABS8_MODEL=demo LEDGER_LATENCY=15s go run . worker &
worker: task queue labs8-refunds, model demo-rules (offline, LABS8_MODEL=demo), memory dgraph at localhost:9080, faults "", latency 15s
--- counter before the turn
llm_calls=5 refund_cases=1 tokens_in=0 tokens_out=0
$ go run . start -session s2 "Open a refund for txn-2026-07-118846 at merchant A-114" &
--- counter during open_refund_case (LEDGER_LATENCY=15s)
llm_calls=7 refund_cases=1 tokens_in=0 tokens_out=0
$ kill -9 3250080
worker is dead
--- counter after kill -9
llm_calls=7 refund_cases=1 tokens_in=0 tokens_out=0
$ LABS8_MODEL=demo LEDGER_LATENCY=15s go run . worker &     # 23:43:30
--- start output                                              # хід завершився о 23:44:06
workflow turn-s2-1793655799249 (session s2)
[ev:tool_call] open_refund_case map[merchant_id:undefined transaction_id:txn-2026-07-118846]
[ev:tool_observation ERROR] open_refund_case map[attempt:1 field:merchant_id observation:ERROR: invalid merchant_id: … status:invalid_argument tool:open_refund_case]
[ev:answer] I should re-extract merchant_id from the request.
[ev:tool_call] open_refund_case map[merchant_id:A-114 transaction_id:txn-2026-07-118846]
[ev:tool_observation] open_refund_case map[case_id:rc-txn-2026-07-118846-A-114 replayed:false status:pending]
[ev:answer] Refund case rc-txn-2026-07-118846-A-114 is open for txn-2026-07-118846 at merchant A-114.
[ev:success] self_corrections=1
--- counter after recovery
llm_calls=8 refund_cases=2 tokens_in=0 tokens_out=0
```

Цей хід зробив 2 виклики LLM до падіння (5 → 7). Після відновлення додався рівно один — фінальна відповідь (7 → 8). Хід без падіння теж робить 3 виклики. Два виклики моделі, зроблені до `kill -9`, Temporal не повторював: їхні результати вже лежали в історії подій, і на replay workflow взяв їх звідти. Повторилась лише Activity `OpenRefundCase`, яка не встигла завершитись. Temporal запустив її знову після `StartToCloseTimeout` (30 с), тому хід закінчився через ~36 с після рестарту. Кейс створено один раз (`refund_cases` 1 → 2 — це новий `txn-…846`). Якби хід почався з нуля, лічильник виріс би ще на 3 (до 10) — це був би не resume, а перезапуск.

## Доказ 3. Сесія продовжується (п. 5)

Другий `start` з тим самим `-session` імпортує попередні події з SQLite (`LoadSession` → `googleadk.ImportSession`). Найкраще це видно на справжній моделі (Gemini з `apps/.env`): у другому ході вона відповідає з історії, без жодного виклику інструмента.

```
$ go run . start -session live1 "Open a refund for txn-2026-07-118847 at merchant A-114"
[ev:tool_call] open_refund_case map[merchant_id:A-114 transaction_id:txn-2026-07-118847]
[ev:tool_observation] open_refund_case map[case_id:rc-txn-2026-07-118847-A-114 replayed:false status:pending]
[ev:answer] A refund case has been successfully opened. … Case ID: rc-txn-2026-07-118847-A-114 …
[ev:success] self_corrections=0

$ go run . start -session live1 "Which case id did you just open, and for which merchant?"
[ev:session] continued: 4 events from earlier turns
[ev:answer] The case ID opened is **rc-txn-2026-07-118847-A-114** for merchant **A-114**.
[ev:success] self_corrections=0

$ go run . counter
llm_calls=16 refund_cases=3 tokens_in=2524 tokens_out=170
```

Наприкінці ходу `googleadk.ExportSession` → Activity `PersistSession` дописує в SQLite лише нові події (за ID), тож повтор Activity нічого не дублює.

## Доказ 4. Пам'ять після повного рестарту (п. 6)

Зупинено воркер, Temporal і Dgraph, потім усе піднято знову, і почато **нову** сесію s3:

```
$ docker compose --profile temporal-pg stop
 Container labs8-temporal-1 Stopped
 Container labs8-postgres-1 Stopped
 Container labs8-dgraph-1 Stopped
$ docker compose --profile temporal-pg up -d --wait
$ LABS8_MODEL=demo go run . worker &
$ go run . start -session s3 "What do you remember about merchant A-114?"
workflow turn-s3-1793655887707 (session s3)
[ev:tool_call] open_nodes map[names:[A-114]]
[ev:tool_observation] open_nodes map[entities:[map[entityType:merchant name:A-114 observations:[…]]] relations:[]]
[ev:answer] From earlier conversations:
- A-114: 2026-11-02T21:44:05Z refund_agent: Refund case rc-txn-2026-07-118846-A-114 is open for txn-2026-07-118846 at merchant A-114.
- A-114: 2026-11-02T21:43:19Z user: Open a refund for txn-2026-07-118846 at merchant A-114
- A-114: 2026-11-02T21:43:04Z refund_agent: Payouts for A-114: 2 payouts pending since 03:10; last settled batch #418 delayed, acquirer says unrelated
- A-114: 2026-11-02T21:43:04Z user: Show the payouts of merchant A-114
- A-114: 2026-11-02T21:42:58Z refund_agent: Refund case rc-txn-2026-07-118845-A-114 is open for txn-2026-07-118845 at merchant A-114.
- A-114: 2026-11-02T21:42:58Z user: Open a refund for txn-2026-07-118845 at merchant A-114
[ev:success] self_corrections=0
```

Пам'ять пише лише Activity `RememberSession` (`AddSessionToMemory`) наприкінці кожного ходу. Читає модель інструментами `open_nodes` / `search_nodes`, і кожен із них теж Activity. Дані Dgraph лежать в іменованому volume, тож `stop` / `down` без `-v` їх не видаляє.

## Офлайн-сценарії збоїв (`go test ./...`)

Кожен сценарій — окрема `TestXxx` на `testsuite.WorkflowTestSuite`, зі скриптованою моделлю (`llm.Demo` або `googleadk.NewFakeModel`) і `ledger.FaultPlan`. SQLite — тимчасовий файл, пам'ять — `graphmemorytest.Fake`.

| Тест | Сценарій | Що перевіряє |
|---|---|---|
| `TestTransientFailuresAreRetriedByTheEngine` | 503, 503 | 3 спроби `OpenRefundCase`, модель не бачить ERROR, 1 кейс |
| `TestNetworkTimeoutIsRetried` | мережевий таймаут × 3 | 4 спроби, кейс відкрито |
| `TestSemanticErrorBecomesObservationAndSelfCorrects` | невалідні аргументи | 2 спроби (без повторів), порядок подій у лозі, `self_corrections=1` |
| `TestDependencyUnavailable` | недоступна залежність (503 × 4) | після `MaximumAttempts` модель отримує `status: unavailable`, 0 кейсів |
| `TestSelfCorrectionBudgetExhausted` | модель ніколи не виправляє аргументи | 4-та помилка → `SelfCorrectionExhausted`, 5-го виклику моделі немає |
| `TestFatalErrorStopsTheTurn` | помилка без класу | `FatalError`, рівно одна спроба |
| `TestRefundCaseIsIdempotent` | той самий кейс двічі | `replayed:true`, 1 кейс |
| `TestSessionContinuesAndMemoryRecalls` | L3 | друга половина сесії бачить першу; нова сесія згадує A-114 |
| `TestClassifyError` | таблиця з 15 рядків | обгортки `%w`, `Canceled`, типи Temporal, «503» лише в тексті → Fatal |

`go test -race -count=5 ./week4/Day8_Fault_Tolerant_Tool_Harness/labs8` — зелений. Покриття за `scripts/covgate.sh` — 85.7%.
