# Практичне завдання 7 — ReAct зсередини: цикл руками vs цикл у графі

**Тиждень 4, частина 1 · Дедлайн: два тижні від відкриття завдання**

> **Станом на 10/2026:** лаба збирається з `google.golang.org/adk/v2` **v2.6.0** (пін у `go.mod`) і Go 1.27.

## Легенда

Ваш агент LEDGERWORKS досі виконує лінійні конвеєри: `prepare → tool → format`, як на Вебінарі 3. Але справжнє розслідування — це **цикл**: подумав → пошукав → подивився на результат → уточнив запит → пошукав знову. А CEO (той самий, що на Вебінарі 6 дивився на Agentic GraphRAG) цього тижня хоче, щоб агент сам обирав, який інструмент викликати, без фіксованого графа.

Ірина Дацюк приходить у четвер у канал і просить: *«Напишіть цикл двічі — спершу руками, потім у графі. Хочу бачити, де наша відповідальність, а де — рушія. І **будь ласка**, додайте жорсткий ліміт ітерацій, бо в нас вже був 3 AM-сценарій на $400».*

## Ранній win: перший видимий результат за ≤15 хвилин

**Спершу — справжній агент на ADK.** Шаблон уже містить робочого ReAct-агента на штатному `llmagent` (файл `labs7/agent.go`: модель, три `functiontool`-інструменти LEDGERWORKS і `BeforeModelCallback`, що обмежує кількість кроків). Запустіть його:

```bash
go run ./week4/Day7_ReAct_Loop_Internals/labs7 agent   # один ран із трасою
go run ./week4/Day7_ReAct_Loop_Internals/labs7 team    # граф із трьох llmagent: ReAct усередині кожного вузла
go run ./week4/Day7_ReAct_Loop_Internals/labs7 console # той самий агент в інтерактивній консолі ADK
```

Зі справжньою моделлю (ключ провайдера в `.env` або `MODEL=…`) перший рядок називає провайдера — `модель: gemini / gemini-3.8-flash (…)` — а далі йдуть кроки, які модель обрала *сама*. Без ключа лаба чесно каже, що працює офлайн, і проганяє скриптовану модель через той самий цикл ADK (вивід скопійовано з реального запуску):

```
модель: офлайн (провайдера не знайдено — жодного провайдера не налаштовано: немає ні ключа, ні MODEL, ні DEFAULT_MODEL_PROVIDER.)
  (скриптована модель: показує форму циклу, а НЕ відповідь моделі)
  act [c1] search_internal_wiki map[query:payout incident]
  act [c2] get_merchant_payouts map[merchant_id:A-114 since:03:00]
  obs [c1] search_internal_wiki: 3 hits: event-1 (rejected), event-2 (merged into incident #418), event-3 (open)
  obs [c2] get_merchant_payouts: 2 payouts pending since 03:10; last settled batch #418 delayed, acquirer says unrelated (since 03:00) …
  act [c3] create_jira_ticket map[body:see incident #418 title:payout incident follow-up]
  obs [c3] create_jira_ticket: LDG-1
answer: Related incident #418 found; acquirer says unrelated. Ticket LDG-1 filed.
model calls: 3, tokens: 1570 (з UsageMetadata)
tickets: [LDG-1: payout incident follow-up / see incident #418]
```

Цей цикл крутите не ви, а рушій: `Flow.Run` в ADK — це `for {}`, який зупиняється лише тоді, коли модель відповіла без tool call. Ліміту кроків у ньому **немає** — його дає колбек в `agent.go`. Сьогодні ви напишете той самий цикл руками, щоб побачити, що саме рушій робить за вас, а що лишається вам.

Далі — той самий сценарій трьома способами. Жоден із них не потребує ані API-ключа, ані акаунта провайдера, ані мережі: планувальник у мануальній версії rule-based, «модель» у графовій — скриптована.

```bash
go run ./week4/Day7_ReAct_Loop_Internals/labs7 graph console
```

Вивід `graph console` (скопійовано з реального запуску) — повний прогін циклу, розписаний рядок за рядком:

```
=== 1. Мануальний цикл (react.go, без ADK) ===
  [1] thought: Check the runbooks and the merchant payouts at the same time; the two reads do not depend on each other
      obs [c1] search_internal_wiki: 3 hits: event-1 (rejected), event-2 (merged into incident #418), event-3 (open)
      obs [c2] get_merchant_payouts: 2 payouts pending since 03:10; last settled batch #418 delayed, acquirer says unrelated (since 03:00) …
  [2] thought: Enough evidence; file a ticket
      obs [c3] create_jira_ticket: LDG-1
  [3] thought: Ticket created, reporting back
  iterations: 3, tokens: — (планувальник без usage)
  answer:  Related incident #418 found; acquirer says unrelated. Ticket: LDG-1
  tickets: [LDG-1: payout incident follow-up / see incident #418]
```

Зверніть увагу на три речі, які ви щойно побачили: **думка** (`thought`) — те, що планувальник вирішив перед дією; **дія** — виклик, позначений `CallID` `c1`/`c2`/`c3`; **спостереження** (`obs`) — те, що повернув зовнішній світ, кожне під своїм `CallID`. Це і є цикл, який ви сьогодні напишете двічі.

Друге: блок `=== 2.` друкує `events: 7` — та сама траса, але зібрана рушієм (у мануальній версії `Trace` ведемо ми, у графовій — ADK). Третє: блок `=== 3.` — продакшн-шлях, де «мозок» — `llmagent` з нативним function calling, а ліміти живуть у колбеках `BeforeModel`/`AfterModel`; він друкує **справжні токени** з `UsageMetadata` (`model calls: 3, tokens: 1570`).

**Те саме зі справжньою моделлю** (потрібен провайдер у `apps/.env`; без нього лаба чесно скаже про це й піде офлайн):

```bash
go run ./week4/Day7_ReAct_Loop_Internals/labs7 graph console
go run ./week4/Day7_ReAct_Loop_Internals/labs7 agent "Check the payout incident for merchant A-114"
```

Перший рядок виводу називає провайдера й модель (`модель: gemini / … (…)`) — за ним завжди видно, чи відповідала модель, чи правила. Локальна Ollama теж працює (`MODEL=ollama/<назва>` або `DEFAULT_MODEL_PROVIDER=ollama`, див. `internal/modelcfg`).

Це не setup і не частина основного завдання: код уже написаний, ви його лише запускаєте й читаєте. Основне завдання починається не з порожнього файлу, а з шаблону `labs7/`: типи й сигнатури вже є, `Run` повертає `ErrNotImplemented`, а контрактні тести в `main_test.go` стоять на `t.Skip`, доки ви їх не ввімкнете. Словник циклу (`ToolCall`, `Step`, `Planner`, `Budget`, `Trace`, реєстр інструментів LEDGERWORKS, `RulePlanner`, `LLMPlanner`) лежить у `labs7/internal/` — читайте, не переписуйте.

## Основне завдання

**Ваше завдання —** зібрати `Native ReAct Agent Core (Go)` у двох варіантах, стартуючи з [`week4/Day7_ReAct_Loop_Internals/labs7/main.go`](https://github.com/dimetron/ai-ae-labs/blob/main/week4/Day7_ReAct_Loop_Internals/labs7/main.go):

Що лежить у шаблоні:

```
labs7/
├── main.go          # тонкий: підкоманди agent | team | graph | console; лише wiring
├── agent.go         # ГОТОВО: штатний ReAct-агент ADK (llmagent + functiontool) — `go run . agent`
├── guards.go        # ГОТОВО: flow guard + LLM guard на одному BeforeToolCallback
├── guards_test.go   # зелені тести порядку охорон: правило раніше за суддю, fail closed
├── team.go          # ГОТОВО: граф із трьох llmagent (fan-out → join → reporter)
├── team_test.go     # зелені тести: ReAct у кожному вузлі, охорона лише на вузлі, що пише
├── agent_test.go    # зелені тести агента: цикл доходить до тікета; ліміт кроків зупиняє модель, що не завершує
├── react.go         # ВИ: мануальний цикл — Run(), поки що повертає ErrNotImplemented
├── graph.go         # ВИ: графова версія — NewDynamicNode + цикл RunNode
├── errors.go        # ErrMaxIterations, ErrLoopDetected — вже визначені (це контракт, не ДЗ)
├── main_test.go     # 2 контрактні тести на t.Skip: ліміт → errors.Is(ErrMaxIterations); порядок CallID зберігається
```

1. **Руками (manual ReAct):** безфреймворковий ReAct-цикл у звичайному Go-коді, файл `react.go`. Контракт:
   ```go
   type ToolCall struct{ CallID, Name string; Args map[string]any }
   type Step     struct{ Thought string; Calls []ToolCall; Final bool; Answer string }
   type Planner  interface{ NextStep(ctx context.Context, h History) (Step, error) }

   func Run(ctx context.Context, p Planner, tools map[string]Tool, task string, b Budget) (string, Trace, error)
   ```
   Цикл: Thought → Action → Observation → …, доки `Step.Final == true` або не вичерпано `Budget`. Крок приймає **один або кілька** tool calls; у кожного стабільний `CallID`, а кожне Observation повертається саме до свого `CallID`. Для core можна виконувати їх послідовно, але не можна втратити або переставити результати.
2. **У графі (graph ReAct):** той самий цикл у `graph.go` через `workflow.NewDynamicNode[IN,OUT]` + `workflow.RunNode[StepResult]`: дочірній вузол-крок `plan_step` викликається в циклі `for` усередині orchestrator-функції, доки не отримано фінальну відповідь. Скелет уже в шаблоні.
3. **≥2 інструменти в обох версіях**, із типізованим контрактом (наприклад, `search_internal_wiki`, `get_merchant_payouts`, `create_jira_ticket` — на вибір). «Мозок» за замовчуванням — справжня модель: `main.go` уже під'єднує `react.LLMPlanner` через `react.ResolveModel`, якщо провайдер налаштований. Без ключа — `RulePlanner` (правила, без LLM), і перший рядок виводу це каже. Тести ви пишете на `RulePlanner` або скриптованій моделі: вони мають проходити без ключа й мережі.
4. **Жорсткий ліміт ітерацій** (константа `maxIterations` ≥ 5) — після вичерпання повертається **явна** `fmt.Errorf("вичерпано ліміт N ітерацій: %w", ErrMaxIterations)`, а не тиха зупинка. Тест на це — обов'язковий.
5. **README** (8–10 речень) з порівнянням: що дає рушій (події, межі вузлів, налаштовані retry/timeout policy, schema-валідація typed tool node), що лишилося на інженері (stop conditions, loop budget, дозвіл дії, кореляція calls/results, idempotency), коли писати цикл руками, коли — у графі. Окремо позначте: незалежні read-only calls можна fan-out-ити, side-effecting calls виконують у явному порядку. Почніть із цієї таблиці й допишіть свої рядки:

   | Рушій бере на себе | Лишається на інженері |
   |---|---|
   | Event log (`session.Event`) для кожного кроку | Умови зупинки та `Budget` (ітерації, токени, час) |
   | Межі вузлів і типізований вхід/вихід `RunNode[StepResult]` | Кореляція calls ↔ results за `CallID` |
   | Налаштовані retry/timeout policy вузла | Дозвіл дії: що агенту взагалі можна викликати |
   | Schema-валідація typed tool node | Ідемпотентність side-effecting calls |

### Чек-ліст здачі

- [ ] Обидві версії компілюються: `go build ./...` — без помилок (порожній вивід).
- [ ] Обидві версії проходять `go test ./...` — без API-ключа (рядок `ok` для кожного пакета).
- [ ] У `main_test.go` прибрано обидва `t.Skip`, і контрактні тести зелені.
- [ ] Графова версія: `go run . graph console` запускається, а тест із планувальником, що ніколи не завершує, зупиняється за `maxIterations` з явною помилкою `вичерпано ліміт N ітерацій` (`errors.Is(err, ErrMaxIterations)` — true).
- [ ] README містить таблицю або список «engine takes / engineer owns» з Блоку 4 лекції.
- [ ] Жоден `git`-коміт не містить API-ключів (`.env` — у `.gitignore`).

## Альтернативні теми (на вибір)

Та сама механіка (цикл із лімітом + ≥2 інструменти), інший домен:

- **Security:** ітеративний аудитор залежностей — цикл «взяти залежність із `go.mod` → перевірити версію → якщо підозріла, пошукати advisory → перейти до наступної» з лімітом. ≥2 інструменти: `parse_go_mod`, `lookup_advisory_db`.
- **Research:** ітеративний пошук відповіді — якщо retrieval із ДЗ 6 повернув слабких кандидатів, агент переформульовує запит і шукає знову (максимум N спроб). ≥2 інструменти: `vector_search`, `graph_traverse`.
- **Fun:** агент-детектив — вгадує загадане слово через цикл «питання → відповідь так/ні → нове питання» з лімітом 10 питань. ≥2 інструменти: `ask_question`, `update_state`.

Альтернативна тема оцінюється за тими самими критеріями, що й основна.

## ++ Advanced (до 20 балів, для сеньйорів)

Оцінюється понад базові 80 балів — будь-яка комбінація пунктів нижче. Це **не** 🔥 Бонус-трек: той не оцінюється взагалі (див. наступний розділ).

- **Детекція зациклення:** повторний tool call з ідентичними `(action, args)` ≥3 рази підряд перериває цикл достроково, *до* `maxIterations`. Повертає окрему `ErrLoopDetected`. Тест: інструмент, що повертає однакове значення, запускається 5 разів — цикл зупиняється на 3-й ітерації.
- **Бенчмарк обох версій** (`go test -bench=BenchmarkReact`): накладні витрати рушія на ітерацію циклу (фіктивний планувальник, що одразу завершує); порівняння `ns/op` для manual vs graph; висновок у README («граф додає ~X ns/op, що = ~Y%»).
- **Справжня модель у вашому циклі:** `react.LLMPlanner` (нативний function calling) у мануальній і графовій версіях — з **токенним бюджетом** з `UsageMetadata` (`Step.Usage`) окремо від лічильника кроків. Ліміт ітерацій, кореляція за `CallID` і детермінований dispatch мають зберегтися на живій моделі, а не лише на правилах; покажіть прогін із перевищенням бюджету. Для graph-версії еталон — `sources/github/adk-go/examples/workflow/dynamic/llm/main.go`.
- **Безпечний fan-out:** виконайте два незалежні read-only calls паралельно й зберіть Observation за `CallID`; два side-effecting calls залиште послідовними та поясніть порядок у тесті або README.
- **Golden-тест на event log:** для графової версії — `go test -update` створює golden файл, `go test` — перевіряє. Захист від drift-у при майбутніх змінах рушія.

## 🔥 Бонус-трек (не оцінюється, не потрібен для сертифіката)

> **Застрягли або хочете контексту?** У лекції є розділ «Відео до теми» — шість необовʼязкових англомовних відео. Два найкорисніші саме зараз: [Understanding AI Agents](https://www.youtube.com/watch?v=i7M7Zs1AzdI) (якщо Thought → Action → Observation ще не вклалось) і [How Monday.com Built Sidekick](https://www.youtube.com/watch?v=c2fLLS7np3Y) (якщо, навпаки, вклалось і цікаво, де цей цикл ламається в проді).

З лекції (розділ «🔥 Бонус-трек»): `maxIterations` **у трьох місцях** при streaming — лічильник у `for`, `context.WithTimeout` на весь ран, token budget як обгортка навколо LLM — плюс integration-тест на 50-iteration runaway, зелений `go test -run TestReact_Runaway ./... -count=10` поспіль. Балів не дає; робиться для себе.

## Якщо щось не працює

Найімовірніші збої й очікуваний вивід на кожному чекпойнті:

| Симптом | Причина | Що зробити · очікуваний результат |
|---|---|---|
| Цикл крутиться до ліміту, хоч інструмент повертає дані | Observation не повертається в історію (`AppendObservations` не викликано) | Перевірте крок 4 циклу. Очікувано: другий `thought` згадує `#418` з першого Observation |
| `ErrMaxIterations` не спрацьовує, тест на runaway зависає | `Run` повертає порожній рядок замість `error`, або `Budget.MaxIterations = 0` без нормалізації | `errors.Is(err, ErrMaxIterations)` має бути `true`; `go test -run TestLoopRunawayHitsIterationCap ./...` → `ok` |
| `go run ./week4/Day7_ReAct_Loop_Internals/labs7 graph console` падає з `manual loop: …` після першого кроку | Планувальник повернув крок без `final` і без `calls` | Переконайтесь, що `RulePlanner.NextStep` віддає або `Final: true`, або ≥1 `ToolCall`. Очікувано: `iterations: 3, tokens: — (планувальник без usage)` і далі блок `=== 2.` |
| `RunNode` → `type mismatch` | Тип-параметр не збігається з `Out` вузла | `workflow.RunNode[StepResult]` для `NewFunctionNode(..., func(...) (StepResult, error))`. Очікувано: `go build ./...` без виводу |
| `go run . agent`, `go run . team` або `go run . console` друкує `модель: офлайн (…)` | Провайдер не налаштований — немає ключа в `.env`, `MODEL` чи `DEFAULT_MODEL_PROVIDER` | Так і задумано: без ключа працює скриптована модель (`agent`, `team`, `console`), а ваша графова версія (`graph`) — правила, і вивід це каже. Для живої моделі додайте ключ або запустіть Ollama |
| `agent`, `team`, `console` або `graph` друкує `модель: офлайн (провайдера не знайдено — …)` | У `apps/.env` немає ключа й `MODEL` | Додайте ключ провайдера або запустіть Ollama (`ollama serve`, `MODEL=ollama/<назва>`). Без цього результати — від правил, не від моделі |
| Живий прогін падає з `Forbidden` / `x509` / `i/o timeout` | Мережевий фільтр (напр. Little Snitch) блокує щойно зібраний Go-бінарник, а `curl` проходить | Дозвольте бінарник у фільтрі або запустіть через `go run` повторно; це не помилка коду |
| Токенний бюджет не спрацьовує, `tokens: —` | Планувальник не звітує usage (правила) або провайдер не повертає `UsageMetadata` | Бюджет у токенах працює лише з моделлю; ліміт ітерацій діє завжди |

## Підказки

1. **Якщо `NewDynamicNode` повертає дивну помилку** — перевірте, що `RerunOnResume` не виставлений у `&false`; для нашого сценарію дефолт `&true` потрібен (інакше resume зламається).
2. **Якщо `RunNode` повертає `nil, error` з текстом "type mismatch"** — перевірте, що `[StepResult]` (тип-параметр) збігається з реальним `Out`-типом `plan_step`. У generic-коді Go «type mismatch» — це помилка виконання, а не компіляції.
3. **Якщо `maxIterations` не спрацьовує** — перевірте, що повертаєте `error` (а не `string` + `nil`) із `RunNode`. Orchestrator-функція має розрізняти «агент відповів» і «сталася помилка».
4. **Якщо планувальник повертає завжди те саме** — це нормально для тесту `maxIterations`. Головне — щоб `Final` ніколи не був `true`, і тоді ліміт спрацює на 100%.

## Критерії оцінювання

| Критерій | Бали |
|---|---|
| Безфреймворковий ReAct-цикл із ≥2 інструментами, динамічним вибором і кореляцією one-or-many calls/results (manual) | 25 |
| Еквівалентна графова версія: `NewDynamicNode` + `RunNode` у циклі (graph) | 25 |
| Ліміт ітерацій в обох версіях з явною помилкою + тест (≥5 ітерацій, `errors.Is` працює) | 20 |
| README з порівняльним аналізом «руками vs runtime» | 10 |
| Альтернативна тема (замість основної) — оцінюється за тими самими критеріями | — |
| ++ Advanced (детекція зациклення, бенчмарк, LLM, golden-тест — будь-яка комбінація) | +20 (бонус) |
| **Разом (максимум)** | **100** |

Базове завдання дає до 80 балів; ++ Advanced — це **+20 додаткових балів** (разом 100).

## Формат здачі

GitHub-репозиторій з кодом обох версій, тестами та README; `go build ./...` має проходити; посилання на репозиторій — у форму здачі. Якщо репозиторій закритий — додайте акаунт ментора в collaborators (акаунт указано в інструкції до курсу на платформі). Шаблон стартової точки: `week4/Day7_ReAct_Loop_Internals/labs7/main.go`. Еталонний приклад для графової версії: [`sources/github/adk-go/examples/workflow/dynamic/basic/main.go`](https://github.com/google/adk-go/blob/v2.6.0/examples/workflow/dynamic/basic/main.go). Шаблон для підключення LLM: [`sources/github/adk-go/examples/workflow/dynamic/llm/main.go`](https://github.com/google/adk-go/blob/v2.6.0/examples/workflow/dynamic/llm/main.go).

## Дедлайн

Два тижні після дати відкриття завдання.

| День лекції | Дедлайн |
|---|---|
| Пн 12.10.2026 | Пн 26.10.2026 |

Шановні слухачі!

Дедлайн надсилання розв'язку на перевірку — 23 год. 59 хв. 26.10.2026 (це крайній термін,
рекомендуємо виконати і надіслати розв'язок протягом тижня після відкриття завдання).

**Інструкція до виконання завдання:**

До 23 год. 59 хв. 26.10.2026 року додайте отримані результати виконання завдання в наступному форматі.

Посилання на репозиторій з виконаним завданням з доданим акаунтом автора до collaborators
https://github.com/dimetron — активне посилання.

Натисніть кнопку «Надіслати відповідь та перейти до наступного етапу». Переконайтесь, що з'явився
напис «Чекаємо оцінки викладача».

Очікуйте на оцінку — згодом вона з'явиться під відповіддю у розділі «Оцінка викладача».

**Зверніть увагу!** Надіслати завдання вдруге неможливо — у вас є лише одна спроба.

На Вебінарі 8 («Надійність агентів: самовідновлення через перехоплення помилок інструментів») ми будемо додавати до вашого ReAct-циклу обробку відмов інструментів — і ваш `maxIterations` стане частиною більшої історії про self-healing. А ваш `Run` на наступній лекції стане Temporal Workflow: ми вб'ємо процес посеред tool call і подивимось, чи агент доведе роботу до кінця.

