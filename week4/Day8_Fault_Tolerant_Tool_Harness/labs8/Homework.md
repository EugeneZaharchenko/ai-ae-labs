# Практичне завдання 8 — Агент, який не панікує: помилка як спостереження

**Тиждень 4, частина 2 · Дедлайн: два тижні від відкриття завдання**

> **Станом на 10/2026:** лаба збирається з `google.golang.org/adk/v2` **v2.6.0** (пін у `go.mod`) і Go 1.27. Durable-шар: `go.temporal.io/sdk` **v1.49.0** + `go.temporal.io/sdk/contrib/googleadk` **v0.3.0** (реліз 30.09.2026). Модуль вимагає тегований ADK ≥ v2.2.0, тому з нашим піном він збирається звичайним `go get`. Локальний сервер — `temporal` CLI 1.9.1 (dev server на вбудованому SQLite). Пам'ять — Dgraph `dgraph/standalone:v25.4.1` + Go-клієнт `github.com/dgraph-io/dgo/v250` v250.0.0. `googleadk` — pre-1.0, API може змінитись: перевірте версію перед стартом.

## Легенда

Минулого тижня (ДЗ 7) ви зібрали ReAct-агента: він думає, викликає інструменти, читає Observation і рухається до мети. Це — автономія. Але автономія має ціну: **агент може приймати неправильні рішення** — викликати інструмент із неправильно розібраними аргументами, у неправильному порядку, у невідповідний момент. О 03:00 Тарас Мельник відкрив прод і знайшов виплату з `merchant_id = 'undefined'`: агент провів платіжну операцію в нікуди.

Того ж ранку команда знайшла другу проблему: воркер агента впав посеред виклику `open_refund_case`. Тарас поставив два питання, на які в команди не було відповіді: *«Ми заплатили двічі? І чи згадає агент завтра, що з мерчантом A-114 уже була ця історія?»*

Звідси три рівні збоїв, і в кожного свій власник:

| Рівень | Збій | Хто відповідає | Механізм у Go |
|---|---|---|---|
| L1 у процесі | транзієнтний збій інструмента (503, таймаут) | рушій | Temporal `RetryPolicy` для Activity |
| L1 у процесі | семантична помилка (`merchant_id='undefined'`) | модель | Observation для моделі, `maxSelfCorrections` |
| L2 падіння процесу | `kill -9` посеред tool call | durable runtime | Temporal Workflow = агентний цикл, Activity = кожен LLM-виклик і кожен tool call (`googleadk`) |
| L3 стан між ранами | розмова продовжується завтра; факти переживають рестарт | персистентність | сесія → SQLite (`.data/agent.db`); довга пам'ять → графова БД (`internal/graphmemory`, Dgraph) |

Правило тижня: **транзієнтні збої лікуються повторами рушія, семантичні помилки повертаються моделі як Observation, і модель сама виправляє свій хід. Усе, що має пережити падіння процесу, живе поза процесом.** Не змішуйте ці механізми. Не мовчіть. Не ігноруйте.

Ще одне правило, без якого L2 не працює: **Workflow не виконує I/O.** Раннер ADK усередині Workflow працює з `session.InMemoryService()`, а стійкість йому дає історія подій Temporal. Усе, що звертається до диска чи мережі (LLM, `open_refund_case`, запис сесії в SQLite, читання й запис пам'яті), — це Activity.

**Ваше завдання —** зібрати `Fault-Tolerant Tool Harness`: захисну обв'язку над інструментами агента. Вона класифікує помилки, має вимірювані метрики й відтворювані тести та доводить дві речі: агент переживає `kill -9` і пам'ятає попередню сесію.

## Ранній win: перший видимий результат за ≤15 хвилин

Це не setup і не частина основного завдання: код уже написаний, ви його лише запускаєте й читаєте. Основне завдання — зібрати те саме у своєму репозиторії, стартуючи з шаблону `labs8/`.

**Крок 0 — запустіть шаблон (ще 5 хвилин).** `labs8/main.go` — це той самий агент в одному процесі, без Temporal і Dgraph. Дві функції в ньому — заглушки, їх ви напишете у п. 1–2. Запустіть як є, офлайн:

```bash
LABS8_MODEL=demo go run . "Open a refund for txn-2026-07-118845 at merchant A-114"
```

Очікуваний вивід заглушки (червоний старт — так і має бути):

```
model: demo-rules (offline, LABS8_MODEL=demo)
[ev:tool_call] open_refund_case map[merchant_id:undefined transaction_id:txn-2026-07-118845]
[ev:tool_observation] open_refund_case map[error:invalid merchant_id: "undefined" was not extracted from the request]
[ev:answer] I could not open the refund case: <nil>
[ev:success] self_corrections=0
```

Рядок `merchant_id:undefined` — це інцидент 03:00: офлайн-модель навмисно «неакуратна» у першому виклику. Заглушка `classifyError` вважає будь-яку помилку фатальною, тож модель не отримала шансу виправитись. Реалізуйте п. 1–2 — і той самий запуск закінчиться так:

```
[ev:tool_call] open_refund_case map[merchant_id:undefined transaction_id:txn-2026-07-118845]
[ev:tool_observation] open_refund_case map[attempt:1 field:merchant_id observation:ERROR: invalid merchant_id: "undefined" was not extracted from the request — please correct your arguments and try again status:invalid_argument]
[ev:answer] I should re-extract merchant_id from the request.
[ev:tool_call] open_refund_case map[merchant_id:A-114 transaction_id:txn-2026-07-118845]
[ev:tool_observation] open_refund_case map[case_id:rc-txn-2026-07-118845-A-114 status:pending]
[ev:answer] Refund case rc-txn-2026-07-118845-A-114 is open for txn-2026-07-118845 at merchant A-114.
[ev:success] self_corrections=1
```

З `-faults 503,timeout` еквайринг двічі відмовить, а повтор (`retry` у шаблоні — стенд-ін для Temporal `RetryPolicy` до п. 3) доведе справу до кінця, а кейс буде створено один раз.

**Справжня модель.** Без `LABS8_MODEL=demo` шаблон і воркер беруть провайдера курсу з `apps/.env` (`internal/modelcfg`, так само як у лабах Тижня 1) і першим рядком друкують, що саме обрано, наприклад `model: gemini → gemini-3.8-flash`. Справжня модель, на відміну від офлайн-правил, зазвичай одразу передає правильний `merchant_id`: щоб побачити Observation, дайте їй дірку у запиті — `go run . "Open a refund for the transaction from yesterday at merchant A-114"` — і подивіться, чи спитає вона номер транзакції, чи вигадає його (`ledger` відхилить вигаданий формат).

## Підготовка середовища (для п. 3–6)

П. 1–2 і всі тести потребують лише Go. Для живого прогону п. 3–6 потрібні два процеси поза вашим агентом. Усі команди виконуйте з каталогу `week4/Day8_Fault_Tolerant_Tool_Harness/labs8`.

```bash
# 1. Temporal dev server на вбудованому SQLite — переживає рестарт (UI: http://localhost:8233)
temporal server start-dev --db-filename .data/temporal.db

# 2. Dgraph — сховище пам'яті, дані в іменованому volume (gRPC :9080)
docker compose up -d dgraph

# 3. Воркер і один хід агента — у двох окремих терміналах (підкоманди, які ви
#    додасте у п. 3-6; тут — ваш власний бінарник лаби)
LABS8_MODEL=demo go run . worker
go run . start -session s1 "Open a refund for txn-2026-07-118845 at merchant A-114"

# 4. Лічильник LLM-викликів і кейсів у .data/agent.db
go run . counter
```

Очікувано після кроку 4 (хід із `kill -9`: `llm_calls=2 refund_cases=0` до падіння, `llm_calls=3 refund_cases=1` після): рядок `llm_calls=N refund_cases=1 tokens_in=… tokens_out=…`. З `LABS8_MODEL=demo` токени дорівнюють 0 (в офлайн-моделі немає лічильника токенів); зі справжнім провайдером вони беруться з `UsageMetadata` відповіді й теж зберігаються в `.data/agent.db`: відновлення не повторює вже завершені виклики моделі, тож токени додаються лише за ще не зроблені виклики.

`LABS8_MODEL=demo` вмикає офлайн-модель на правилах. Без цієї змінної воркер використовує провайдера з `apps/.env` (так само, як лаби Тижня 1) і на старті друкує рядок `worker: … model live (gemini-3.8-flash) …` з назвою моделі. Немає ключа — воркер зупиняється одразу, а не на першому ході, і підказує `LABS8_MODEL=demo`. Збої вмикає змінна `LEDGER_FAULTS` (наприклад, `503,503,timeout`), затримку інструмента — `LEDGER_LATENCY` (наприклад, `15s`).

Опційно: Temporal на PostgreSQL 16 замість SQLite — `docker compose --profile temporal-pg up -d`. Для оцінки це не потрібно; так виглядає production-конфігурація.

Сесії й лічильник лежать у файлі `.data/agent.db` (додайте `.data/` у `.gitignore`). Пам'ять — у Dgraph. Temporal тримає власний файл `.data/temporal.db`, і ваш код у нього не пише.

## Основне завдання

Зберіть `Fault-Tolerant Tool Harness`, стартуючи з [week4/Day8_Fault_Tolerant_Tool_Harness/labs8/main.go](https://github.com/dimetron/ai-ae-labs/blob/main/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/main.go). Пакети в `labs8/internal/` уже готові, їх не треба писати: `ledger` (інструменти LEDGERWORKS, черга збоїв `FaultPlan`, ключ ідемпотентності), `agentdb` (SQLite-сховище сесій і лічильника), `graphmemory` (пам'ять на Dgraph), `llm` (офлайн-модель і лічильник викликів).

1. **Класифікація помилок через `errors.Is/As`** (15 балів). Реалізуйте `classifyError(err) → (Class, string)` із трьома класами: `ClassRetryTransient` (віддати рушію), `ClassSurfaceToModel` (повернути моделі як Observation), `ClassFatal` (негайна зупинка). `context.Canceled` — це `ClassFatal`: це сигнал зупинитись, а не причина повторити виклик. Типи `ledger.TransientError` і `ledger.SemanticError` мають метод `Is`, тому `errors.Is(err, ledger.ErrTransient)` працює крізь будь-які обгортки `%w`. Не створюйте паралельні sentinel-и.
2. **Observation + ліміт самокорекції** (15 балів). Семантична помилка повертається моделі як Observation (`{"tool": …, "status": "invalid_argument", "field": …, "observation": …}`), а не як `error`. Константа `maxSelfCorrections` дорівнює 3. Після вичерпання ліміту хід завершується явною помилкою, що обгортає `ErrSelfCorrectionExhausted`, а не тихою зупинкою. У README — лог одного ходу з самокорекцією: `[ev:tool_call] → [ev:tool_observation ERROR] → [ev:tool_call з виправленими аргументами] → [ev:success]`. Це — відповідь Тарасу на питання «що зробив агент?».
3. **Durable-цикл** (20 балів). Агент із ДЗ 7 працює всередині `AgentWorkflow(ctx workflow.Context, req Request)`. Раннер ADK отримує контекст `googleadk.NewContext(ctx)`, модель — `googleadk.NewModel(...)`, тому кожен LLM-виклик стає Activity. Інструмент `open_refund_case` викликає Activity `OpenRefundCase` з `RetryPolicy{NonRetryableErrorTypes: []string{"SemanticError", "FatalError"}}`. Через межу worker → workflow проходить лише рядок типу помилки, а не Go-тип, тому workflow класифікує помилку за цим рядком. Транзієнтні збої повторює Temporal; семантичні до повтору не доходять. Запис кейсу ідемпотентний: ключ = `transaction_id + merchant_id`, повторний виклик повертає `replayed=true`, а не другий кейс.
4. **Доказ crash-safe** (10 балів). Лічильник LLM-викликів зберігається **поза** процесом — у `.data/agent.db`. Запустіть хід із `LEDGER_LATENCY=15s`, під час виклику інструмента вбийте воркер `kill -9`, перезапустіть його. Хід доходить до кінця, а `go run . counter` показує рівно стільки викликів, скільки зробив би хід без падіння: ті, що були до `kill -9`, плюс лише ті, яких ще не було (з офлайн-моделлю: 2 до падіння, 3 після — додалася лише фінальна відповідь; контрольний хід без падіння теж дає 3). Якщо лічильник виріс більше — ви не відновились, ви **перезапустились**. У README — копія терміналу: лічильник до `kill -9`, сам `kill -9`, лічильник після відновлення.
5. **Персистентна сесія** (10 балів). На початку ходу Activity `LoadSession` читає знімок сесії з SQLite, а workflow імпортує його через `googleadk.ImportSession`. Наприкінці ходу workflow експортує сесію (`googleadk.ExportSession`), і Activity `PersistSession` записує знімок у SQLite. Другий `start` з тим самим `-session` продовжує розмову, а не починає її з порожньої історії.
6. **Персистентна пам'ять** (10 балів). Наприкінці ходу Activity `RememberSession` віддає сесію в `graphmemory` через `AddSessionToMemory` — це єдиний шлях запису. Модель читає пам'ять інструментами `search_nodes` (пошук за словами) і `open_nodes` (усе про відомий id, наприклад `A-114`); кожен інструмент виконує Activity. Бекенд за замовчуванням — `MEMORY_BACKEND=dgraph`. Доказ у README: зупиніть воркер, Temporal і Dgraph (`docker compose stop`), запустіть усе знову, почніть **нову** сесію. Агент згадує факт про A-114 з **попередньої** сесії.

**Обов'язкова умова для п. 1–3:** ≥3 сценарії збоїв як офлайн-тести: мережевий таймаут, невалідні аргументи, недоступна залежність. Кожен сценарій — окрема `TestXxx` на `testsuite.WorkflowTestSuite` зі скриптованою моделлю й `ledger.FaultPlan`. Усі тести проходять **без** API-ключа, **без** Docker і **без** сервера Temporal.

**Опційно: тест зі справжньою моделлю.** Додайте тест, який проганяє той самий workflow з моделлю курсу й перевіряє інваріанти, а не слова відповіді: рівно один кейс після двох збоїв еквайрингу, щонайменше три спроби `OpenRefundCase`, пам'ять відповідає в новій сесії. Тримайте його вимкненим за замовчуванням і не оцінюйте ним: `LAB_LIVE=1 go test -run Live -v ./...`. Без провайдера або з відхиленим ключем тест має пропускатися й називати причину.

> **Чому не власний журнал подій?** Durable execution можна зібрати й без Temporal: append-only файл `NDJSON`, у який дописується результат кожного завершеного кроку. Під час старту агент спершу програє журнал і пропускає вже виконані кроки. Це та сама ідея у 100 рядках, і розібратись у ній корисно. Але оцінюється шлях через Temporal: він дає повтори, таймаути й історію подій, які в журналі довелося б писати самому.

## Що має бути в репозиторії

```
labs8/
├── main.go            # тонкий: передає os.Args у run()
├── app.go             # підкоманди worker, start, counter; конфігурація з env
├── harness.go         # classifyError, maxSelfCorrections, ErrSelfCorrectionExhausted
├── tools.go           # open_refund_case, get_merchant_payouts, search_nodes, open_nodes + RetryPolicy
├── workflow.go        # AgentWorkflow — раннер ADK на googleadk.NewContext
├── activities.go      # OpenRefundCase, LoadSession, PersistSession, RememberSession, SearchNodes, OpenNodes
├── *_test.go          # classifyError (таблиця) + ≥3 сценарії збоїв на testsuite
├── compose.yaml       # сервіс dgraph (іменований volume); профіль temporal-pg (опційно)
├── internal/          # готові пакети: ledger, agentdb, graphmemory, llm
└── README.md          # лог самокорекції, лог kill -9, доказ пам'яті після рестарту
```

Ви обираєте бекенд пам'яті змінною `MEMORY_BACKEND`, а не пишете його. Власний бекенд — це ++ Advanced.

## Альтернативні теми (на вибір)

Та сама механіка (harness + класифікація + Observation + durable-цикл + пам'ять), інший домен:

- **Security:** harness для HTTP-перевірок безпеки (заголовки, TLS-версія, certificate transparency). Хости не відповідають, перенаправляють, повертають некоректні дані. Агент розрізняє «хост недоступний» (транзієнтний збій) і «хост неправильний» (семантична помилка — перенаправлення на фішинговий домен) і пам'ятає хости, які вже перевірив.
- **Research:** harness для збирача веб-джерел (ДЗ 6). Частина URL недоступна або повертає HTML-капчу. Агент шукає альтернативні джерела, не зациклюється й не перевіряє вдруге вже зібране.
- **Fun:** «кав'ярня в годину пік». Інструменти замовлення відмовляють («молоко закінчилося», «кавоварка зламалась»). Агент пропонує заміну, обмежує спроби й наступного дня пам'ятає улюблений напій гостя.

## ++ Advanced (до 20 балів, для сеньйорів)

Оцінюється понад базові 80 балів — будь-яка комбінація пунктів нижче. Це окрема від 🔥 Бонус-треку (нижче) частина: бали дає саме ++ Advanced.

- **Метрики відновлюваності**: mass-fault сценарій (≥10 інструментів × ≥20 ходів із різними типами збоїв) у `metrics_test.go`: recovery rate, mean retries-to-success, time-to-diagnosis. Метрики — у golden-файл, щоб CI ловив регресію. Поріг recovery rate ≥ 0.85 — домовленість цього курсу, а не галузевий стандарт; один лише поріг знизу не ловить тихий цикл повторів (ран із 50 повторами, що зрештою вдався, теж дає 1.0), тому додайте стелю на retries-to-success.
- **Другий бекенд пам'яті** (Neo4j, Memgraph, FalkorDB, Postgres, SQLite — або AGE, NornicDB, SurrealDB), що проходить `graphmemorytest.Run` разом із reopen-тестом. Порівняйте recall і затримку того самого агента на двох бекендах.
- **Пам'ять як MCP-сервер**: винесіть `search_nodes` / `open_nodes` в окремий MCP-сервер і підключіть його через `googleadk.NewMCPToolset`.
- **Операційний граф Neo4j** через `github.com/neo4j/mcp` v1.6.0 у `googleadk.NewMCPToolset` (лише читання).
- **HITL-підтвердження** `open_refund_case` через `PendingConfirmations` + signal.
- **Continue-as-new** для довгих розмов (`ExportSession` / `ImportSession`).
- **Replay-safe OpenTelemetry**: спани, які не дублюються під час replay.

## 🔥 Бонус-трек (не оцінюється, не потрібен для сертифіката)

Це завдання на власним бажанням: балів за нього немає. Якщо хочете, щоб глибина пішла в оцінку — робіть `++ Advanced` вище.

- **`RateLimitError` з HTTP-інтеграцією** (з лекції, «🔥 Бонус-трек»): обгортка над `http.Client` розбирає заголовок `Retry-After` і повертає `&RateLimitError{RetryAfter: ...}`. Harness використовує цю підказку замість фіксованого backoff. Почніть із `LEDGER_FAULTS=429`.
- **Власний механізм повторів з експоненційним backoff + jitter**, порівняний із Temporal `RetryPolicy`: коли дефолту досить, а коли потрібен свій (підказка: дефолт не знає про `RateLimitError.RetryAfter`).
- **Порівняння з Pi Durable (без коду).** Прочитайте розділи «Persist and Resume» і «Tools» у [README `pi-durable`](https://github.com/earendil-works/pi/blob/main/packages/durable/README.md). У README своєї лаби назвіть інструменти агента, які ви б позначили `replay: "safe"`. Поясніть, чому `open_refund_case` можна так позначити лише разом із ключем ідемпотентності.

Найкоротший вхід у тему durable execution — 20 хвилин відео з розділу «Відео до теми» лекції: [The Invincible MCP Server](https://www.youtube.com/watch?v=pYGD8YqYxP0), фрагмент **00:19:52–00:20:11**, де це сказано одним рядком: «tool call — це Activity».

> **Про що це завдання насправді.** Не про Temporal. «Агент впав і перезапустився» коштує ще одного повного проходу по платному API. «Агент впав і відновився» не коштує нічого. На одному ході різниці не видно. На 10 000 ходів на місяць це різниця в рахунку.

## Якщо щось не працює

Найімовірніші збої й очікуваний вивід на кожному чекпойнті:

| Симптом | Причина | Що зробити · очікуваний результат |
|---|---|---|
| `errors.Is(err, ErrTransient)` повертає `false`, хоч помилка транзієнтна | Обгортання через `%v`, а не `%w` | Замініть на `fmt.Errorf("…: %w", err)`. Очікувано: `go test -run TestClassifyError ./...` → `PASS` |
| Семантична помилка йде в повтор замість Observation | Рядка `SemanticError` немає в `NonRetryableErrorTypes`, або Activity повертає помилку без типу | Повертайте `temporal.NewApplicationError(…, "SemanticError")`; додайте тип у `NonRetryableErrorTypes`. Очікувано: у лозі Observation без повторів |
| Після повтору в базі два кейси | Запис без ключа ідемпотентності | Ключ = `transaction_id + merchant_id`. Очікувано: `refund_cases=1`, у відповіді `replayed=true` |
| Агент виправляється нескінченно | Немає ліміту самокорекції або лічильник глобальний | `maxSelfCorrections = 3`, лічильник у стані ходу. Очікувано: `errors.Is(err, ErrSelfCorrectionExhausted) = true` |
| Тести нестабільні (flaky) між запусками | Збої через `rand`, час із `time.Now()` | Використайте `ledger.FaultPlan.Push(...)` замість ймовірності. Очікувано: `go test -count=10 ./...` зелений 10/10 |
| Після рестарту воркера: `nondeterministic workflow` / `[TMPRL1100]` | Код Workflow виконує I/O, `time.Now()` або `rand` | Перенесіть I/O в Activity; час і UUID беріть через `googleadk.NewContext(ctx)`. Очікувано: хід доходить до кінця після рестарту |
| Лічильник LLM-викликів після `kill -9` = N+1 | LLM викликається напряму, оминаючи `googleadk.NewModel` | Модель — лише через `googleadk.NewModel`. Очікувано: до падіння N, після — N плюс лише ще не зроблені виклики (з `demo`: 2 → 3, як у ході без падіння) |
| Воркер або шаблон: `немає LLM-провайдера` / `live model: …` | Жодного ключа в `apps/.env` і в оточенні | Додайте ключ за зразком `apps/.env-example` або запустіть офлайн: `LABS8_MODEL=demo`. Очікувано: перший рядок виводу називає модель |
| `API key not valid` / `401` / `insufficient_quota` від провайдера | Ключ прострочений або кінчився кредит | Це не помилка harness-а. Перевірте ключ; на час ДЗ достатньо `LABS8_MODEL=demo`, усі тести й так офлайн |
| Хочу локальну модель через Ollama | — | Задайте `OLLAMA_BASE_URL` (і `OLLAMA_MODEL`, напр. `qwen3.5:4b-mlx`) у `apps/.env`; `internal/modelcfg` обере її. Слабка локальна модель частіше помиляється в аргументах — це гарний тест для `maxSelfCorrections` |
| `graphmemory: MEMORY_BACKEND=dgraph: cannot reach DGRAPH_ADDR=localhost:9080` | Dgraph не запущений | `docker compose up -d dgraph`. Очікувано: воркер стартує без помилки |
| `temporal at localhost:7233 (is \`temporal server start-dev\` running?)` | Temporal не запущений | Запустіть `temporal server start-dev --db-filename .data/temporal.db`. Очікувано: `start` друкує event log |
| Нова сесія нічого не пам'ятає | `RememberSession` не викликається наприкінці ходу, або `docker compose down -v` видалив volume | Викликайте `RememberSession` наприкінці кожного ходу; зупиняйте через `stop` або `down` **без** `-v`. Очікувано: `search_nodes` знаходить A-114 |

## Підказки, які зекономлять вам годину

1. **Не створюйте паралельні sentinel-и.** Використовуйте `ledger.ErrTransient` / `ledger.ErrSemantic` і типи `ledger.TransientError` / `ledger.SemanticError`. Інакше `errors.Is` не розпізнає помилку крізь обгортки.
2. **`fmt.Errorf("...: %w", err)` — не `%v`**. Лише `%w` зберігає ланцюг для `errors.Is/As`. Це найчастіша помилка першої спроби.
3. **Go-тип помилки не проходить межу worker → workflow.** Проходить лише рядок типу `ApplicationError`. Тому на боці workflow класифікуйте за рядком, а не через `errors.Is`.
4. **Тестуйте `classifyError` окремо від усього іншого.** Це чиста функція; її покриває таблиця з 6 рядків. Без неї тести на сценарії збоїв нічого не доводять.
5. **Лічильник самокорекцій — не глобальний.** Тримайте його в стані ходу. Глобальний лічильник ламає паралельні тести.
6. **Код Workflow — лише координація.** Усе, що читає чи пише щось за межами пам'яті процесу, — Activity. Якщо сумніваєтесь, спитайте себе: «що станеться, якщо цей рядок виконається вдруге під час replay?»

## Анти-патерни, які ми побачимо в рев'ю (і відхилимо)

- `try/catch`-стиль через `panic/recover` у Go — це не ідіоматично й не видно в event log.
- Один великий `switch err.Error()` замість `errors.Is/As` — ламається на обгортках.
- Семантична помилка повертається як `error` без типу — модель не побачить Observation, а рушій повторюватиме виклик.
- Відсутній ліміт самокорекції — агент зациклюється на кожному поганому вводі.
- Запис у SQLite чи Dgraph напряму з коду Workflow — replay виконає його вдруге.
- Тести, що вимагають API-ключа, Docker або сервера Temporal для перевірки `classifyError` чи сценаріїв збоїв. Це інтеграційні тести, а не тести harness. `testsuite.WorkflowTestSuite` і скриптована модель існують саме для того, щоб цього уникнути.

## Критерії оцінювання

| Критерій | Бали |
|---|---|
| Класифікація `classifyError` через `errors.Is/As`: транзієнтні → повтори рушія, семантичні → Observation, `context.Canceled` → Fatal | 15 |
| Observation для семантичних помилок + `maxSelfCorrections` з явною `ErrSelfCorrectionExhausted`; лог самокорекції в README | 15 |
| Durable-цикл: `AgentWorkflow`, `googleadk.NewModel`, Activity `OpenRefundCase` з `NonRetryableErrorTypes`, ідемпотентний запис кейсу | 20 |
| Доказ crash-safe: `kill -9` посеред tool call, лічильник LLM-викликів не зріс | 10 |
| Персистентна сесія: `LoadSession` / `PersistSession` у SQLite, другий `start` продовжує розмову | 10 |
| Персистентна пам'ять: `RememberSession` + recall через `search_nodes` / `open_nodes` після повного рестарту | 10 |
| ≥3 офлайн-сценарії збоїв (`testsuite` + скриптована модель + `FaultPlan`) — обов'язкова умова для перших трьох рядків | — |
| ++ Advanced (метрики в golden, другий бекенд пам'яті, MCP-сервер пам'яті, HITL, continue-as-new, OTel — будь-яка комбінація) | +20 (бонус) |
| **Разом (максимум)** | **100** |

Базове завдання дає до 80 балів; ++ Advanced — це **+20 додаткових балів** (разом 100). Альтернативна тема оцінюється за тими самими критеріями.

🔥 Бонус-трек — це вправа для сеньйорів без окремого бала: додаткові бали дає ++ Advanced.

## Формат здачі

GitHub-репозиторій з кодом, тестами та README. `go build ./...` і `go test ./...` мають проходити без API-ключа, Docker і Temporal. README містить три докази: лог самокорекції, лог `kill -9` з незмінним лічильником і recall з попередньої сесії після рестарту. Посилання на репозиторій — у форму здачі. Якщо репозиторій закритий — додайте акаунт ментора в collaborators (акаунт указано в інструкції до курсу на платформі).

Стартовий шаблон: [week4/Day8_Fault_Tolerant_Tool_Harness/labs8/main.go](https://github.com/dimetron/ai-ae-labs/blob/main/week4/Day8_Fault_Tolerant_Tool_Harness/labs8/main.go). Еталонні приклади: [`temporalio/samples-go` → `googleadk/`](https://github.com/temporalio/samples-go/tree/main/googleadk) (базовий приклад у корені, плюс `humanintheloop` і `chat`: ADK усередині Workflow), [`sources/github/adk-go/examples/multiagent/collaboration/`](https://github.com/google/adk-go/blob/v2.6.0/examples/multiagent/collaboration/main.go) (functiontool, типізований інструмент).

## Дедлайн

Два тижні після дати відкриття завдання.

| День лекції | Дедлайн |
|---|---|
| Чт 15.10.2026 | Чт 29.10.2026 |

Шановні слухачі!

Дедлайн надсилання розв'язку на перевірку — 23 год. 59 хв. 29.10.2026 (це крайній термін,
рекомендуємо виконати і надіслати розв'язок протягом тижня після відкриття завдання).

**Інструкція до виконання завдання:**

До 23 год. 59 хв. 29.10.2026 року додайте отримані результати виконання завдання в наступному форматі.

Посилання на репозиторій з виконаним завданням з доданим акаунтом автора до collaborators
https://github.com/dimetron — активне посилання.

Натисніть кнопку «Надіслати відповідь та перейти до наступного етапу». Переконайтесь, що з'явився
напис «Чекаємо оцінки викладача».

Очікуйте на оцінку — згодом вона з'явиться під відповіддю у розділі «Оцінка викладача».

**Зверніть увагу!** Надіслати завдання вдруге неможливо — у вас є лише одна спроба.

Якщо ви плануєте ++ Advanced із другим бекендом пам'яті — закладіть +1 день на інтеграцію.


## Після здачі

Після виконання ДЗ 8 ви готові до вебінару 9, де ми візьмемо ваш агент із персистентною сесією та пам'яттю й розберемо, **як цей стан змінюється**: `StateDelta` в ADK 2.0, правила розв'язання конфліктів і якість пам'яті. Питання Тараса «а де живе стан, якщо процес упав» ви вже закрили в цьому ДЗ; наступне питання — «чи можна цьому стану вірити».
