# Практичне завдання 3 — Агент стає графом: вузли, ребра, тести

> **Станом на 09/2026 (перед використанням потрібно перевірити):** пін `google.golang.org/adk/v2` = v2.5.0, Go 1.27 — брати з `go.mod` лабораторії, не `@latest`. Сигнатури `workflow.NewFunctionNode`, `workflow.NewToolNodeTyped`, `workflow.Chain`, `agent.NewStrictContextMock` **звірено дослівно з модулем v2.5.0 01.10.2026** — точні рядки й файли в `references.md` (внутрішній докладний звіт звірки, не публікується).

> **Одна пастка компіляції, яку варто знати заздалегідь.** `workflow.NewFunctionNode` повертає **одне** значення, а `workflow.NewToolNodeTyped` — **два** (`*ToolNode, error`). Це друга за частотою причина «не збирається» в цьому ДЗ.

## Легенда

Оксана з комплаєнсу LEDGERWORKS не може довести, чи агент справді відкрив кейс повернення мерчанту A-114, чи лише ввічливо про це написав. Спершу запустіть публічний тест нижче: він порівнює `LlmAgent` зі скриптованою моделлю та детермінований граф. У звичайному `go run` використовується реальний провайдер; безмодельний шлях обирається явно через `-mode=graph`. Один typed tool лишається спільним для обох шляхів.

**Наскрізний інструмент курсу вже у стартері.** ДЗ 2 було в іншому домені (курси валют, `get_exchange_rate`), тому переносити звідти нічого не треба. Пакет `week2/internal/refund` постачає `open_refund_case`: `refund.Input{TransactionID, MerchantID}` → `refund.Output{CaseID, TransactionID, MerchantID, Status}`. Ваша робота — адаптація графа й тести, не нове тіло tool. Фікстури: `txn-2026-07-118845`, `A-114`, кейс `rc-txn-2026-07-118845-A-114`.

## Ранній win: перший видимий результат за ≤15 хвилин

Перш ніж писати власний код, переконайтеся, що середовище живе. З кореня `ai-ae-labs`:

```bash
go test -v -run TestEventLogIsAuditable ./week2/Day3_First_ADK2_Agent_Workflow_Graph/labs3
```

**Очікуваний результат** (станом на 09/2026; час залежить від машини):

```
=== RUN   TestEventLogIsAuditable
=== RUN   TestEventLogIsAuditable/LlmAgent
=== RUN   TestEventLogIsAuditable/workflow-граф
--- PASS: TestEventLogIsAuditable (0.00s)
    --- PASS: TestEventLogIsAuditable/LlmAgent (0.00s)
    --- PASS: TestEventLogIsAuditable/workflow-граф (0.00s)
PASS
ok  	github.com/dimetron/ai-eng-course/labs/week2/Day3_First_ADK2_Agent_Workflow_Graph/labs3
```

Без API-ключа й викликів провайдера. Два підтести доводять **той самий аудит-слід** у контрольованому LLM-шляху та графі. Зелені тести підтверджують цей локальний шлях, не доступність чи якість live-провайдера.

Хочете побачити стабільний лог графа, а не тільки `PASS`:

```bash
go run ./week2/Day3_First_ADK2_Agent_Workflow_Graph/labs3 -mode=graph
```

## Основне завдання

**Ваше завдання —** розкласти агента LEDGERWORKS на явний workflow-граф ADK 2.0, де кожен виклик `open_refund_case` є обов'язковим вузлом і лишає подію в event log, а не рішенням, яке ухвалює сама модель.

Зберіть `ADK 2.0 First Agent (Go)` як явний граф зі стартового шаблону [week2/Day3_First_ADK2_Agent_Workflow_Graph/labs3/main.go](https://github.com/dimetron/ai-ae-labs/blob/main/week2/Day3_First_ADK2_Agent_Workflow_Graph/labs3/main.go):

1. Прочитайте й адаптуйте наданий статичний потік `Start → prepare → open_refund_case → format` у [`agent_graph.go`](agent_graph.go): `workflow.NewFunctionNode`, `workflow.NewToolNodeTyped`, `workflow.Chain`. Кроки й інструмент беруться з `week2/internal/refund`; топологію графа композирує сама лаба.
2. Переконайтеся, що граф працює **без API-ключа** (`go run . -mode=graph console` з каталогу `labs3`). Звичайний `go run .` / `go run . console` використовує реальну модель через `internal/modelcfg`; скриптовані відповіді існують тільки в тестах.
3. Покрийте вузли табличними тестами на `agent.NewStrictContextMock` (див. `agent/context_mock.go`): щонайменше по 2 кейси на `prepare` і `format`, включно з помилковим входом. Для tool-handler перевірте успішний `StateDelta` і порожній `StateDelta` при помилці.
4. Додайте в README нормалізований фрагмент event log одного рану та розділ «Що дає граф проти imperative-скрипта» (5–7 речень). Не вигадуйте формат `[ev:*]`: зафіксуйте `session.Event` / `StateDelta` через власний стабільний formatter.

## Альтернативні теми (на вибір)

Та сама механіка (граф `FunctionNode → ToolNode → FunctionNode` + табличні тести), інший домен:

- **Security:** конвеєр-ревʼюер конфігів — `parse_yaml → check_rules → report`: знаходить у Kubernetes-манифесті privileged-контейнери та відсутні resource limits.
- **Research:** конспектор статей — `fetch_url → extract_text → summary_stub`: витягує текст сторінки й рахує статистику (слова, заголовки, посилання).
- **Fun:** граф-бариста — `parse_order → price_calculator → receipt`: перетворює «два лате і круасан» на типізований чек.

## ++ Advanced (до 20 балів, для сеньйорів)

- Умовна маршрутизація: додайте вузол-класифікатор із `workflow.StringRoute` (як у `examples/workflow/routing/string`), що обирає між двома інструментами залежно від запиту.
- Валідація схем вузла через `workflow.NewFunctionNodeWithSchema` + тест, що невалідний вхід відсікається до виконання функції.

## 🔥 Бонус-трек (не оцінюється, не потрібен для сертифіката)

- **Третій маршрут.** Розширте два маршрути `workflow.StringRoute` до трьох (refund / статус транзакції / поза доменом) і додайте по одному табличному тест-кейсу на кожен. Це розширення балів не дає; базові два маршрути й schema-validation test належать до ++ Advanced.

## Якщо щось не працює

1. **`go build` падає на імпортах** — перевірте, що імпортуєте `google.golang.org/adk/v2/tool/functiontool`, а не пакет іншої версії ADK; потім `go mod tidy`.
2. **`NewToolNodeTyped` повертає помилку при збірці графа** — схема не виводиться з типів: звірте теги `json:"..."` і `jsonschema:"..."` на полях Input/Output.
3. **`go run . console` не стартує** — порівняйте `go version` із директивою `go` у `go.mod`; якщо launcher падає, `l.CommandLineSyntax()` покаже доступні підкоманди.
4. **Golden-тест «мигає» між ранами** — ви порівнюєте сирий stdout з нестабільними ID/timestamp; нормалізуйте формат власним formatter-ом і перевіряйте порядок business-подій та `StateDelta`.
5. **Live-run не стартує, але тести зелені** — перевірте provider/model у `apps/.env` або environment. Для ізоляції проблеми запустіть `-mode=graph`; автоматичного fallback на fake немає.

## Критерії оцінювання

| Критерій | Бали |
|---|---|
| Робочий граф `Start → prepare → tool → format` через `workflow.Chain` | 30 |
| `open_refund_case` підключено через `NewToolNodeTyped[refund.Input, refund.Output]` | 15 |
| Табличні тести на `StrictContextMock`, усі зелені (≥4 кейси) | 25 |
| README: нормалізований event log фрагмент + «граф vs скрипт», зафіксовані версії `adk/v2` і Go | 10 |
| ++ Advanced (бонус) | 20 |
| **Разом** | **100** |

Базове завдання дає до 80 балів; ++ Advanced — це **+20 додаткових балів** (разом 100), він не є умовою сертифіката. Альтернативна тема оцінюється за тими самими критеріями. Бали за всі 12 завдань підсумовуються: від 60 % відкривається генерація сертифіката, від 85 % — з відзнакою.

## Формат здачі

GitHub-репозиторій з кодом, тестами та README; `go build ./...` і `go test ./...` мають проходити; посилання на репозиторій — у форму здачі. Якщо репозиторій закритий — додайте акаунт ментора в collaborators (акаунт указано в інструкції до курсу на платформі).

Робоча основа й режими запуску: [README](README.md). Для власного форку збережіть кореневі `go.mod`/`go.sum`, `internal/` та `week2/internal/`; один `main.go` не є окремим модулем. Еталонні API-приклади: [workflow/basic](https://github.com/google/adk-go/blob/v2.5.0/examples/workflow/basic/main.go), [workflow/routing/string](https://github.com/google/adk-go/blob/v2.5.0/examples/workflow/routing/string/main.go).

## Дедлайн

Два тижні після дати відкриття завдання.

Шановні слухачі!

Дедлайн надсилання розв'язку на перевірку — 23 год. 59 хв. 09.10.2026 (це крайній термін,
рекомендуємо виконати і надіслати розв'язок протягом тижня після відкриття завдання). Наступна
лекція спирається на цей артефакт, тому раннє надсилання допомагає вам самим.

**Інструкція до виконання завдання:**

До 23 год. 59 хв. 09.10.2026 року додайте отримані результати виконання завдання в наступному форматі.

Посилання на репозиторій з виконаним завданням з доданим акаунтом автора до collaborators
https://github.com/dimetron — активне посилання.

Натисніть кнопку «Надіслати відповідь та перейти до наступного етапу». Переконайтесь, що з'явився
напис «Чекаємо оцінки викладача».

Очікуйте на оцінку — згодом вона з'явиться під відповіддю у розділі «Оцінка викладача».

**Зверніть увагу!** Надіслати завдання вдруге неможливо — у вас є лише одна спроба.

Слухачам, які служать у ЗСУ, дедлайн продовжуємо за окремим запитом.
