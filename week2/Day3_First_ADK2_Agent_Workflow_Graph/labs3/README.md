# Лабораторна 3 — граф, який залишає перевірюваний слід

**Станом на 09/2026:** Go 1.27.1, ADK Go v2.4.0; точні залежності — у кореневому `go.mod`.
Потрібен Go. Тести та явний `-mode=graph` не потребують ключа чи LLM; **звичайний запуск використовує реальну модель**. Перше завантаження Go-модулів потребує мережі.

## Перший результат

Команди нижче виконуйте **з кореня `ai-ae-labs`**, де лежить `go.mod`:

```bash
go test -v -run '^TestEventLogIsAuditable$' ./week2/Day3_First_ADK2_Agent_Workflow_Graph/labs3
go run ./week2/Day3_First_ADK2_Agent_Workflow_Graph/labs3 -mode=graph
```

Очікуємо два зелені підтести: `LlmAgent` зі скриптованою моделлю та `workflow-граф`.
Друга команда друкує три нормалізовані JSON-події **графа**, зокрема:

```json
{"author":"first_graph_agent","output":{"case_id":"rc-txn-2026-07-118845-A-114","merchant_id":"A-114","status":"pending","transaction_id":"txn-2026-07-118845"},"state_delta":{"refund:last_case_id":"rc-txn-2026-07-118845-A-114","refund:last_merchant_id":"A-114","refund:last_status":"pending"}}
```

ID виклику й час прибрано, але `output` і `state_delta` взято з реальних `session.Event`.
Інтерактивний граф без моделі:

```bash
go run ./week2/Day3_First_ADK2_Agent_Workflow_Graph/labs3 -mode=graph console
```

Введіть `Мерчант A-114 просить повернення по транзакції txn-2026-07-118845`.
Повторіть запит у тому самому процесі: статус стане `already_open`, ID кейса не зміниться.
`Ctrl+C` завершує консоль.

## Звичайний запуск — реальна модель, не mock

Налаштуйте провайдера як у Тижні 1: `apps/.env` або явні environment variables, `DEFAULT_MODEL_PROVIDER` та `MODEL`. Завантаження й gateway-routing виконує існуючий `internal/modelcfg`; секрети не записуйте в README.

```bash
go run ./week2/Day3_First_ADK2_Agent_Workflow_Graph/labs3
go run ./week2/Day3_First_ADK2_Agent_Workflow_Graph/labs3 console
```

Перша команда робить один реальний `Runner + LlmAgent + open_refund_case` прогін із лімітом 60 секунд; друга відкриває діалог. Це може коштувати токени. Перевіряйте `content.functionCall`/`functionResponse` та `state_delta`, а не конкретне формулювання моделі.
При відсутній конфігурації чи помилці провайдера запуск завершується з причиною — **не перемикається на fake або граф мовчки**.

Тести викликають **той самий** `newLiveAgent`, але передають `fakellm.Model` замість провайдера. Фейк імпортується тільки з `*_test.go`. «Реальний» тут означає inference і виконання нашого Go-tool; реєстр кейсів усе ще навчальний in-memory, не платіжне API.

## Де що змінювати

| Файл | Призначення |
|---|---|
| [`main.go`](main.go) | Вибір live/graph, одноразовий прогін або ADK launcher |
| [`agent.go`](agent.go) | `LlmAgent` з ін'єкцією моделі та реальним refund-tool |
| [`agent_test.go`](agent_test.go) | Один інструмент у двох топологіях; стабільність аудит-демо |
| [`../../internal/refund/refund.go`](../../internal/refund/refund.go) | `Input`/`Output`, реєстр, `Prepare`, `OpenCase`, `Format`, `NewGraph` |
| [`../../internal/refund/refund_test.go`](../../internal/refund/refund_test.go) | Табличні тести вузлів на `StrictContextMock`, помилки, повтори й конкурентність |

Граф уже запускається: `Start → prepare → open_refund_case → format`.
Це робоча основа для пояснення й модифікації у [завданні](Homework.md), не порожній шаблон.
Спільний пакет розташований у `week2/internal/refund`, щоб Lab 4 використовувала **той самий код**, а не копію без аудит-подій.

```bash
go build ./week2/...
go test -race ./week2/...
go vet ./week2/...
```

## Межі навчальної моделі

Реєстр відкриває **кейс**, а не переказує гроші. Він і сесії живуть у пам'яті процесу.
Непрефіксовані ключі стану зберігаються між ходами однієї сесії, не після перезапуску.
Помилкові ID не змінюють бізнес-стан. Повторів вузлів немає: локальну валідацію не виправити backoff-ом.

Подія підтверджує зафіксований результат у цій моделі. Відсутність події сама по собі не доводить відсутність зовнішнього платежу після аварії: для реального API потрібна звірка з бізнес-системою.

## Якщо щось не працює

- **Не знаходить пакет:** поверніться в корінь репозиторію, не в каталог окремого файлу.
- **Стара версія Go:** перевірте `go version` проти кореневого `go.mod`.
- **Запит відхилено:** потрібні обидва ID; підтримані мерчанти `A-114` та `B-207`.
- **Немає звичного `tool_call` у графі:** ToolNode повертає `Event.Output`; перевіряйте також `StateDelta`, а не лише LLM function calls.
- **Після перезапуску знову `pending`:** це очікувана межа in-memory реєстру.

Для власної роботи форкніть репозиторій і збережіть кореневі `go.mod`/`go.sum`, `internal/` та `week2/internal/`. Один `main.go` не є самодостатнім модулем.
