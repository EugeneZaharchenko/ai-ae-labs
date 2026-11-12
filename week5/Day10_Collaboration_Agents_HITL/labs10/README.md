# ДЗ 10 — Collaborative Workflow Engine команди LEDGERWORKS

Станом на 11/2026: Go 1.27 · `google.golang.org/adk/v2 v2.5.0` (пін у `go.mod`; усі API з умови — `NewJoinNode`, `AddFanOut` / `AddFanIn`, `NewEmittingFunctionNode`, `ResumeOrRequestInput`, `RerunOnResume` — звірено за module cache v2.5.0) · жива модель — Gemini `gemini-3.8-flash` через `internal/modelcfg`.

Ірина просить план міграції з legacy-процесора для ради директорів до понеділка 18:00, підпис Оксани обов'язковий. Три спеціалісти збирають факти паралельно, координатор пише підсумок, а Оксана бачить **доказ** (дія, отримувачі, хеш, діф, факти, джерела, прогалини) і вирішує. Без її схвалення в журналі документ не надсилається.

## Як запустити

```bash
cd week5/Day10_Collaboration_Agents_HITL/labs10
go run .                                        # demo: start + відповідь зі stdin в одному процесі
go run . -session board-0909 start              # процес 1: ран до паузи, друкує доказ і ID запиту
go run . pending                                # що чекає рішення (читає журнал)
go run . -by oksana decide <id> approve         # процес 2: рішення в журнал + відновлення рану
go run . -by oksana decide <id> deny
go run . -down integration_desk start           # джерело integration_desk мовчить → прогалина
go run . -window 30s start ; go run . sweep     # мовчання довше за вікно → timed_out
go run . audit board-0909                       # події рану, журнал рішень, токени
```

Прапорці: `-db` (`.data/labs10.db`: сесії ADK і журнал в одному SQLite), `-outbox` (`.data/outbox`), `-window` (2m), `-by` (oksana), `-user` (iryna), `-session`, `-down`. Без провайдера перший рядок — `ОФЛАЙН-ФЕЙК …`, і кожен агент отримує власний скрипт `fakellm`. Один спільний скрипт не підходить: паралельні гілки забирали б одна в одної його ходи.

## Архітектура

```
START → plan ─┬─> risk_desk        (llmagent + lookup_risk_desk)        ─┐
              ├─> compliance_desk  (llmagent + lookup_compliance_desk)  ─┼─> gather (JoinNode) → merge → coordinator → draft → approval_gate → notify_board
              └─> integration_desk (llmagent + lookup_integration_desk) ─┘                          (llmagent)            (HITL-пауза)  (ризикова дія)
```

| Вузол | Тип | Що робить |
|---|---|---|
| `plan` | `NewFunctionNode` | віддає `Plan{Topic, Desks, Deadline}`; `Desks` — це й порядок розділів |
| `risk_desk`, `compliance_desk`, `integration_desk` | `NewAgentNode(llmagent)` | кожен має **свій** `functiontool` і свою фікстуру; `AfterToolCallback` записує справжній результат інструмента в state `desk:<name>` |
| `gather` | `NewJoinNode` | бар'єр: `AddFanIn` перелічує всіх трьох; далі йде `map[desk]проза` |
| `merge` | `NewFunctionNode` (`Engine.Merge`) | розділи **в порядку `Plan.Desks`**, факти беруться зі state, а не з прози |
| `coordinator` | `NewAgentNode(llmagent)` | підсумок 3–5 речень лише з `verified`-розділів + рядок `ПРОГАЛИНА: …` на кожен неперевірений |
| `draft` | `NewFunctionNode` | рендерить документ, рахує `sha256`, діф до останньої відправленої версії, пише чернетку `drafts/<hash>.md` |
| `approval_gate` | `NewEmittingFunctionNode` + `ResumeOrRequestInput`, `RerunOnResume: true` | `Ask` у журнал → пауза → після відновлення читає рішення **з журналу** |
| `notify_board` | `NewFunctionNode` (`notifyBoard`) | дія виконується лише з `approved` у журналі та без прогалин; `Spend(id, hash, recipients)` дозволяє одну відправку |

- **Координатор і спеціалісти.** Хто вирішує наступний крок, визначає граф, а не модель. Координатор нічого не маршрутизує: він лише формулює підсумок. План фіксований, тож порядок розділів однаковий у кожному рані.
- **Режими.** Усі `llmagent` стоять у вузлах графа без власного `mode`, тобто працюють як single_turn: одна задача, вхід = вихід попередника. Режими chat і task тут не потрібні, бо розмова з людиною йде через затвор, а не через агента.
- **Схеми.** Між вузлами ходять типізовані Go-структури: `Plan` → `Brief{Sections}` → `Draft` → `GateResult` → `Outcome`. Інструменти мають схеми `deskInput` / `deskOutput{Findings[]{claim, source}}`, які `functiontool` генерує з тегів. Питання людині описує схема `Evidence`.

| Файл | Зміст |
|---|---|
| `desks.go` | три спеціалісти: фікстури, інструменти, інструкція «інструмент першим, лише факти з джерелом», `recordOutcome`, `SourceError` |
| `engine.go` | `NewEngine` (граф), `Merge`, `draft`, `notifyBoard` |
| `gate.go` | `Evidence`, `newApprovalGateNode`, `parseVerdict`, `awaitDecision` + сентинели |
| `cli.go` | команди `demo/start/pending/decide/sweep/audit`, офлайн-скрипти |
| `trace.go` | рядок на подію (хто / гілка / що), токени по авторах |
| `internal/approvals` | журнал (дано). Я додав лише `History(session)` для `audit` + тест |

## Чому спеціалістів три, а не один

1. **Різні інструменти й джерела.** Одному агентові з трьома інструментами модель сама вирішує, які з них викликати, і може тихо пропустити один. Тут кожен спеціаліст має рівно один інструмент і правило «виклич його першим». Якщо модель інструмент не викликала, розділ стає прогалиною `NO_TOOL_CALL`.
2. **Ізоляція контексту.** Статичний fan-out дає кожній гілці власну під-гілку (`risk_desk@1`, `compliance_desk@1`, `integration_desk@1`). Факти CRM не потрапляють у промпт спеціаліста з комплаєнсу, і джерела не змішуються. Це перевіряє `TestSpecialistsDoNotSeeEachOther`: тест читає, що саме отримала кожна фейкова модель. Координатор отримує зведення всіх трьох розділів, і це теж перевірено.
3. **Різні збої.** Таймаут CRM не валить ризики й комплаєнс. Збій локалізовано в одному розділі з власним кодом.
4. **Паралельність.** `TestDesksRunInParallel` ставить бар'єр у моделі кожного спеціаліста: перший виклик кожного чекає, доки стартують усі три. Якби гілки йшли послідовно, тест упав би за 2 с. У живому рані три виклики інструментів стартують за 1 с (`12:14:19.0 / 19.4 / 19.98`).

**Коли один агент кращий.** Коли джерело одне або інструментів 1–2. Коли кроки залежать один від одного: «спершу знайди мерчанта, потім перевір його комплаєнс» — тут паралелити нічого. І коли дорожчає кожен токен: інструкція й промпт тут повторюються тричі (≈1000 prompt-токенів на спеціаліста, див. таблицю нижче). Один агент простіше налагоджувати, і для короткої задачі в нього менша латентність.

## Заземлення і стримування збою (п.1, п.6)

Розділ будується **не з тексту моделі**. `AfterToolCallback` (`recordOutcome`) бачить справжню Go-помилку інструмента і записує в state `desk:<name>` JSON `{ok, findings}` або `{code, error}`. Модель від тієї ж помилки бачить лише `{"error": "…"}` і може переказати її як завгодно. Далі `merge` вирішує: розділ `verified` лише тоді, коли інструмент відповів і кожен факт має джерело. Будь-який інший шлях дає названу прогалину:

| Код | Коли |
|---|---|
| `INTEGRATION_TIMEOUT` / `INCIDENT_DB_TIMEOUT` / `COMPLIANCE_REGISTER_TIMEOUT` | джерело не відповіло (`*SourceError`, прапорець `-down`) |
| `NO_TOOL_CALL` | спеціаліст відповів без виклику інструмента |
| `NO_FINDINGS` | інструмент повернув порожньо |
| `UNSOURCED` | факт без джерела |
| `BAD_OUTCOME` / `BAD_TOOL_OUTPUT` / `TOOL_ERROR` | запис не читається / інша помилка |

Офлайн-фейк `integration_desk` при мовчазному джерелі навмисно «бреше»: «Усе гаразд: мерчанти готові до міграції.» Розділ однаково стає прогалиною, а документ не містить ні цієї фрази, ні фактів CRM (`TestSpecialistFailureDoesNotCorruptTheAnswer`). Живий Gemini у тому ж сценарії чесно написав `ДЖЕРЕЛО НЕ ВІДПОВІЛО: INTEGRATION_TIMEOUT`, але система на його чесність не покладається.

Захист має три шари:

1. Доказ для людини показує прогалину: `розділ: integration_desk: unverified … INTEGRATION_TIMEOUT` і `ПРОГАЛИНИ: … approve заблоковано`.
2. `decide … approve` для документа з прогалиною повертає помилку і **нічого не пише** в журнал:
   ```text
   approve заблоковано: документ має неперевірений розділ: integration_desk; можливе лише deny
   ```
3. `notify_board` не надсилає документ із прогалиною, навіть якщо в результаті затвора стоїть `approved` (той самий тест перевіряє виклик в обхід `decide`).

Ще два запобіжники:

- **Координатор приховав прогалину.** Якщо підсумок не згадує неперевірений розділ, доказ отримує `unnamed_gaps`, і людина бачить рядок `УВАГА: підсумок координатора не називає прогалину integration_desk` (`TestCoordinatorHidingAGapIsFlagged`).
- **Мовчать усі джерела.** Затвор узагалі не питає людину: `запит на схвалення без конкретного доказу: документ без жодного розділу; risk_desk: INCIDENT_DB_TIMEOUT …` (`TestAllSourcesDownGivesNoRequest`).

Живий ран із `-down integration_desk` — підсумок координатора в документі:

```text
## Підсумок координатора
Останній QSA-аудит 2026-06-14 було пройдено без зауважень (audit:qsa-2026-06), проте згідно з вимогами PCI DSS 4.0.1
PAN має бути токенізовано до перенесення даних карт (pci-dss:req-3.5.1). … попередній cutover 2025-03 відкотили за 41 хв
через розсинхрон токенів (incident-db:INC-418) …

ПРОГАЛИНА: integration_desk — INTEGRATION_TIMEOUT: джерело integration-crm не відповіло

## Мерчанти на legacy-інтеграції (integration_desk)
ПРОГАЛИНА (INTEGRATION_TIMEOUT): джерело integration-crm не відповіло. Розділ не заповнено.
```

## Затвор схвалення (п.3)

**Перший прохід `approval_gate`:**

1. `buildEvidence(draft)` збирає доказ.
2. `Store.Ask` пише запит з дедлайном. Ключ `InterruptID = board-pack-<InvocationID>`: він стабільний для цього рану й унікальний між ранами.
3. `ResumeOrRequestInput` випускає `RequestInput` з тим самим доказом у `Payload` і повертає `ErrNodeInterrupted`.

Ран стає на паузу, процес завершується. Стан вузлів графа і події лежать у тому самому SQLite, що й журнал (`approvals.Store.Sessions()`).

**`decide` в іншому процесі:**

1. Спершу пише рішення в журнал (`Decide`, умовний UPDATE `status = pending`, тому рішення записується рівно один раз).
2. Потім відновлює ран: надсилає `FunctionResponse{Name: adk_request_input, ID: <interrupt>}`.
3. Затвор перезапускається (`RerunOnResume`), а `Ask` нічого не змінює: дедлайн лишається першим. Відповідь із сесії затвор **не вважає рішенням** і читає `Store.Get(id)`. Якщо в журналі досі `pending`, тобто хтось відновив ран в обхід журналу, затвор пише `invalid by system:gate` (`TestResumeWithoutLedgerDecisionIsInvalid`).

**Доказ (`Evidence`) містить лише факти:**

- `action`, `recipients`, `artifact` (шлях до чернетки, яку пінить хеш);
- `hash`, `diff` (рядки, що зникли / з'явилися відносно останньої відправленої версії);
- `risk_facts`, статус кожного розділу з кодом, `sources`, `gaps`;
- `approve_allowed`.

Прози координатора в доказі немає.

**Живий сценарій «start в одному процесі → decide в іншому»:**

```text
$ go run . -session board-0909 start
модель: gemini → gemini-3.8-flash
сесія: board-0909
…
ЗАПИТ НА СХВАЛЕННЯ board-pack-e-c72903e0-815e-4a31-97b0-37dba74cfac1 (статус pending, вікно до 12:16:35)
  дія:        send_board_pack → .data/outbox/drafts/563fa1ac5e011b4d.md
  отримувачі: board@ledgerworks.example, iryna@ledgerworks.example
  хеш:        sha256:563fa1ac5e011b4d5f1ade48633e2ebef3818cf9103edace8fd9d5bd95eff8af
  діф:        (перша версія: попередньої відправленої немає)
  розділ:     risk_desk: verified, фактів 3
  розділ:     compliance_desk: verified, фактів 3
  розділ:     integration_desk: verified, фактів 2
  ризик:      Cutover 2025-03 відкотили за 41 хв через розсинхрон токенів (incident-db:INC-418)
  ризик:      Rollback-ранбук для A-114 перевірено востаннє у вересні (runbook:cutover-rollback)
  ризик:      Пік legacy-процесора — 1840 TPS у п'ятницю ввечері (capacity:legacy-q3)
  джерела:    incident-db:INC-418, runbook:cutover-rollback, capacity:legacy-q3, pci-dss:req-3.5.1, nbu:payments-notice-30d, audit:qsa-2026-06, crm:legacy-merchants-2026-10, crm:A-114
ран на паузі. Рішення з іншого процесу: go run . -by oksana decide board-pack-e-c72903e0-… approve|deny

$ go run . -by oksana decide board-pack-e-c72903e0-815e-4a31-97b0-37dba74cfac1 approve
журнал: board-pack-e-c72903e0-815e-4a31-97b0-37dba74cfac1 → approved (by oksana)
дія ВИКОНАНО: надіслано [board@ledgerworks.example iryna@ledgerworks.example], схвалив oksana
```

**Сценарій reject** (живий ран із прогалиною):

```text
$ go run . decide board-pack-e-a1dc52a4-… approve
approve заблоковано: документ має неперевірений розділ: integration_desk; можливе лише deny
$ go run . decide board-pack-e-a1dc52a4-… deny
журнал: board-pack-e-a1dc52a4-5415-4b01-9b06-1b91b65393e1 → denied (by oksana)
дія НЕ ВИКОНАНО: дію не виконано: denied (людина відмовила)
```

Що я перевірив експериментом. Повторне відновлення з тим самим `InterruptID` у ADK v2.5.0 **ще раз запускає** і затвор, і `notify_board`. Отже «не надіслати двічі» гарантує не граф, а журнал: `notifyBoard` перед записом викликає `Spend(id, hash, recipients)`, а хеш перераховує з тексту документа. Друге відновлення отримує `схвалення вже використано`. Змінений документ отримує `документ змінився після схвалення`. Якщо відправка впала, `Unspend` повертає схвалення і пише причину в журнал (`TestPauseSurvivesRestartAndApprovalIsOneShot`, `TestNotifyBoardReturnsTheApprovalWhenSendFails`).

## Вікно рішення: мовчання = відмова (п.4)

`Ask` пише `ExpiresAt = now + window`. `sweep` викликає `Store.Expire`: прострочений запит стає `timed_out by system:timeout` з причиною `ніхто не відповів до <час>`. Після цього ран відновлюється, і `notify_board` дію не виконує. У `demo` відповідь чекає `awaitDecision(ctx, replies, window, by)`, і кожен шлях без чіткого «так» дає відмову з власною причиною (`errors.Is`):

| Шлях | Помилка | Запис у журналі |
|---|---|---|
| людина відповіла `deny` | — | `denied`, by `oksana`, «людина відмовила» |
| ніхто не відповів у вікні | `ErrDecisionTimeout` | `timed_out`, by `system:timeout` |
| контекст скасовано | `ErrDecisionCancelled` | `timed_out`, by `system:cancel` |
| UI закрив канал (stdin EOF) | `ErrRepliesClosed` | `timed_out`, by `system:ui` |
| UI надіслав незрозуміле (`мабуть`, `👍`) | `ErrUnrecognizedReply` | `invalid`, by `oksana`, «нерозпізнана відповідь» |
| відновлення в обхід журналу | — | `invalid`, by `system:gate` |

Тести: `TestApprovalTimeoutMeansRefusal` (вікно 30 мс, канал, у який ніхто не пише, + ще п'ять шляхів), `TestSilenceEndsTheRunAsTimedOut` (повний ран: вікно 30 мс → `sweep` → `timed_out`; пізній `approve` → `ErrAlreadyDecided`), `TestThreeRefusalsAreThreeRecords`. Офлайн-вивід:

```text
$ go run . -window 2s -session t-silent start ; sleep 2.2 ; go run . sweep
журнал: board-pack-e-d6dc0802-… → timed_out (ніхто не відповів до 2026-11-09T10:16:22Z)
дія НЕ ВИКОНАНО: дію не виконано: timed_out (ніхто не відповів до 2026-11-09T10:16:22Z)

$ go run . decide board-pack-e-856ebda2-… мабуть
журнал: board-pack-e-856ebda2-3b7c-4c5c-8e9c-818247857b2f → invalid (by oksana)
дія НЕ ВИКОНАНО: дію не виконано: invalid (нерозпізнана відповідь: "мабуть")
```

## Трасування: один повний ран (п.5)

`go run . audit board-0909` після живого рану вище (Gemini, два процеси):

```text
== події сесії board-0909 (19)
 1  12:14:15.753  user             -                   текст: Підготуй план міграції з legacy-процесора для ради директорів: …
 2  12:14:15.760  ledgerworks_team -                   вихід plan: desks=[risk_desk compliance_desk integration_desk]
 3  12:14:19.004  risk_desk        risk_desk@1         виклик lookup_risk_desk(map[topic:План міграції з legacy-процесора …])
 4  12:14:19.017  risk_desk        risk_desk@1         інструмент lookup_risk_desk → 3 фактів
 5  12:14:19.399  integration_desk integration_desk@1  виклик lookup_integration_desk(map[topic:…])
 6  12:14:19.412  integration_desk integration_desk@1  інструмент lookup_integration_desk → 2 фактів
 7  12:14:19.984  compliance_desk  compliance_desk@1   виклик lookup_compliance_desk(map[topic:…])
 8  12:14:19.997  compliance_desk  compliance_desk@1   інструмент lookup_compliance_desk → 3 фактів
 9  12:14:22.231  integration_desk integration_desk@1  текст: 37 мерчантів досі на legacy API v1 (ISO 8583 через TCP) (crm:legacy-merchants-2026-10) …
10  12:14:22.307  risk_desk        risk_desk@1         текст: Cutover 2025-03 відкотили за 41 хв через розсинхрон токенів (incident-db:INC-418) …
11  12:14:23.972  compliance_desk  compliance_desk@1   текст: PCI DSS 4.0.1: PAN має бути токенізовано до перенесення даних карт (pci-dss:req-3.5.1) …
12  12:14:23.976  ledgerworks_team -                   вихід gather: зібрано compliance_desk, integration_desk, risk_desk
13  12:14:23.984  ledgerworks_team -                   вихід merge: risk_desk=verified compliance_desk=verified integration_desk=verified
14  12:14:35.976  coordinator      -                   текст: Під час попереднього cutover 2025-03 процедуру відкотили за 41 хв …
15  12:14:35.989  ledgerworks_team -                   вихід draft: hash=sha256:563fa1ac5e011…
16  12:14:36.000  ledgerworks_team -                   ПАУЗА: запит людині board-pack-e-c72903e0-815e-4a31-97b0-37dba74cfac1
17  12:14:41.898  user             -                   ВІДНОВЛЕННЯ: відповідь approve на board-pack-e-c72903e0-815e-4a31-97b0-37dba74cfac1
18  12:14:41.904  ledgerworks_team -                   вихід approval_gate: status=approved by=oksana
19  12:14:41.915  ledgerworks_team -                   вихід notify_board: sent=true надіслано [board@ledgerworks.example iryna@ledgerworks.example], схвалив oksana
== журнал рішень (1)
board-pack-e-c72903e0-815e-4a31-97b0-37dba74cfac1  send_board_pack  .data/outbox/drafts/563fa1ac5e011b4d.md → approved
  запит 12:14:35, вікно до 12:16:35, рішення 12:14:41 by oksana, використано 12:14:41
  хеш sha256:563fa1ac5e011b4d5f1ade48633e2ebef3818cf9103edace8fd9d5bd95eff8af, отримувачі board@ledgerworks.example, iryna@ledgerworks.example
  причина: людина схвалила
```

Колонки: час події, автор, гілка (`-` — коренева), що сталося. Номери рядків я додав для пояснення.

- **1** — повідомлення Ірини запускає ран (процес 1).
- **2** — `plan` фіксує склад команди й порядок розділів. Автор `ledgerworks_team`, бо функційні вузли пише сам workflow-агент. Вузол у трасі впізнається за формою виходу.
- **3–8** — **паралельні гілки.** Три спеціалісти на трьох різних гілках `<desk>@1` викликають кожен свій інструмент протягом ~1 с, і рядки перемежовуються. `→ N фактів` — відповідь інструмента, яку побачив `recordOutcome`.
- **9–11** — прозові відповіді спеціалістів. Кожен факт має джерело в дужках, як вимагає інструкція. Порядок завершення (integration → risk → compliance) не збігається з планом.
- **12** — `gather` (JoinNode) спрацьовує один раз, коли відзвітували всі три. Ключі мапи впорядковано абеткою, а не планом.
- **13** — `merge` переставляє розділи в порядок плану (risk → compliance → integration) і ставить статус кожному.
- **14** — координатор на кореневій гілці отримує зведений `Brief` і пише підсумок (≈12 с: найдовший виклик рану).
- **15** — `draft`: документ, хеш, діф, чернетка на диску.
- **16** — **пауза.** Затвор записав запит у журнал і випустив `RequestInput`. Процес 1 завершився на цьому рядку.
- **17** — **процес 2** (`decide`) через 6 с: рішення вже в журналі, відповідь `approve` лише будить затвор.
- **18** — затвор прочитав рішення з журналу (`approved by oksana`), а не з рядка 17.
- **19** — `notify_board`: `Spend` пройшов (хеш і отримувачі збігаються, схвалення невикористане), документ записано в `outbox/board-pack.md`.

Рядок журналу відповідає на питання «хто / що / коли»: що просили (дія, артефакт, хеш, отримувачі), коли спитали, до коли чекали, хто й коли вирішив, коли схвалення використано, з якою причиною.

## Облік токенів (п.7)

Сума `UsageMetadata` подій по авторах; той самий живий ран (друкується після `start` і в `audit`):

```text
токени (UsageMetadata подій):
  risk_desk        викликів=2 prompt=1054 output=133 total=1560
  integration_desk викликів=2 prompt=1006 output=100 total=1408
  compliance_desk  викликів=2 prompt=1074 output=143 total=1641
  coordinator      викликів=1 prompt=864 output=336 total=2479
  РАЗОМ            prompt=3998 output=712 total=7088
```

- **2 виклики в спеціаліста:** перший — виклик інструмента, другий — відповідь після результату.
- **`total > prompt + output`:** Gemini 3 рахує в `TotalTokenCount` ще й thinking-токени (`thoughtsTokenCount`), які я окремо не друкую.
- **Офлайн:** `токени: модель не повернула UsageMetadata (офлайн-фейк не рахує токени)` — чесна причина, а не нулі.

## Тести і перевірки

| Тест | Що доводить |
|---|---|
| `TestJoinNodeWaitsForAllBranches` | JoinNode — бар'єр: вузол після join запускається один раз і бачить і швидку, і повільну (50 мс) гілку |
| `TestDesksRunInParallel` | три спеціалісти справді паралельні (бар'єр у моделях; послідовний запуск падає за 2 с) |
| `TestSpecialistsDoNotSeeEachOther` | промпт спеціаліста не містить інструментів і джерел сусідів; координатор бачить усі три розділи |
| `TestMergeKeepsPlanOrderAndNamesEveryGap` | порядок розділів — з плану; коди `BAD_OUTCOME`, `NO_TOOL_CALL`, `UNSOURCED`, `NO_FINDINGS` |
| `TestSpecialistFailureDoesNotCorruptTheAnswer` | прогалина з кодом у доказі й документі, без вигаданого; approve заблоковано; `notify_board` не надсилає |
| `TestAllSourcesDownGivesNoRequest` | без жодного розділу затвор не питає, помилка перелічує всі причини |
| `TestCoordinatorHidingAGapIsFlagged` | підсумок без прогалини → `unnamed_gaps` і попередження людині |
| `TestPauseSurvivesRestartAndApprovalIsOneShot` | start → закрити Store → новий Store → decide approve → надіслано рівно той документ; повтор не надсилає |
| `TestDenyDoesNotSend`, `TestResumeWithoutLedgerDecisionIsInvalid` | reject і відновлення в обхід журналу не виконують дію |
| `TestApprovalTimeoutMeansRefusal`, `TestSilenceEndsTheRunAsTimedOut`, `TestThreeRefusalsAreThreeRecords` | п.4 (див. таблицю вище) |
| `TestNotifyBoardReturnsTheApprovalWhenSendFails` | збій запису → `Unspend` з причиною; змінений документ не використовує старе схвалення |
| `TestAuditShowsPauseResumeBranchesAndLedger`, `TestRunCommands`, … | траса, журнал, команди CLI, токени |
| `TestLiveTeamGroundsSectionsAndContainsTheGap` | живий тест: Gemini викликає інструменти, `integration_desk` стає прогалиною |

```text
gofmt -l .            → порожньо
go vet ./...          → ок
go test -race ./...   → ok labs10, ok labs10/internal/approvals
covgate 85            → labs10 90.0%, internal/approvals 86.1% (без func main)
LAB_LIVE=1 go test -run Live -v .   → --- PASS: TestLiveTeamGroundsSectionsAndContainsTheGap (16.57s), модель: gemini → gemini-3.8-flash
go test -run Live -v .              → --- SKIP: живий тест: задайте LAB_LIVE=1 (потрібен провайдер у apps/.env)
```

Живий тест я **запускав** (PASS, вище). Без `LAB_LIVE=1` він пропускається з названою причиною, і такий skip не означає «пройшло».

## Чесна межа HITL: де б я зупинився на ADK, а де пішов би в Temporal

Пауза в ADK тут — це збережений стан вузлів у сесії плюс рядок журналу. Для паузи на хвилини чи години, де хтось гарантовано запустить `decide` або `sweep`, цього достатньо: пауза переживає рестарт процесу (тест і живий ран вище). Але сама система ніколи не прокидається. Таймаут «спрацьовує» лише тоді, коли хтось запустить `sweep` (cron), а між `Spend` і записом у outbox є вікно: якщо процес упаде саме там, схвалення буде витрачене, а лист — ні. `Unspend` рятує лише від акуратної помилки, не від kill -9.

Для паузи на 12+ годин із можливим падінням воркера я залишив би на ADK граф, спеціалістів, доказ і промпти, а очікування і ризикову дію переніс би в Temporal-workflow:

- **Очікування.** Затвор стає `workflow.AwaitWithTimeout(ctx, window, …)` на Signal (або Update з валідатором, який відхиляє approve з прогалиною ще до запису). Durable timer замінює `sweep`, і мовчання стає відмовою в історії workflow без зовнішнього cron.
- **Відправка.** `notify_board` стає Activity з idempotency key `interrupt_id + hash` і RetryPolicy. Падіння воркера посеред відправки закінчується повтором тієї самої activity, а не «витраченим» схваленням.
- **Спеціалісти.** Вони можуть лишитися ADK-агентами всередині activity: у `go.mod` вже є `go.temporal.io/sdk/contrib/googleadk v0.3.0`, який використовує labs8 з ДЗ 8.

Межа така: ADK вирішує, *що* сказати й показати людині, а Temporal гарантує, що чекання і дія *доживуть* до відповіді.

## Що я свідомо залишив на потім

- **++ Advanced:** Fault-Tolerant Tool Harness з ДЗ 8 в інструментах спеціалістів і `BeforeToolCallback` як policy-точка. Не робив, бо в цій здачі лише базове завдання. Абзац про Temporal вище є, бо його вимагає формат здачі.
- **A2A** для зовнішніх підагентів і authn/authz на `notify_board` (бонус-трек): зараз будь-хто з доступом до SQLite може записати `approved`.
- **Durable execution** — див. абзац вище; таймаут залежить від `sweep`.
- **Multi-critic цикл** над підсумком координатора. Зараз перевіряється лише те, що він називає прогалини. Те, що кожне речення спирається на факт розділу, не перевіряється.
- **Ретраї й таймаути на рівні вузла** для повільних моделей (як `resilientModel` у прикладі `workflow/complex`).
- **Справжній діф (LCS)** замість різниці множин рядків; UI для рішення замість CLI.
- `start` завжди створює **нову** сесію: ключі `desk:<name>` у state живуть у межах сесії, і повторний ран у тій самій сесії прочитав би старі результати.

Розбіжність з умовою: Homework згадує еталон `labs10/solution/` (`go run ./solution demo`), але в репозиторії курсу його немає (публікується після дедлайну). Тому все побудовано за прикладами ADK v2.5.0 з module cache: `workflow/complex`, `workflow/hitl_rerun`, `multiagent/collaboration`.
