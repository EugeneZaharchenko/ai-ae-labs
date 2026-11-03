# Приклади графових робочих процесів

Запускні приклади для **графового рушія** adk-go (`google.golang.org/adk/v2/workflow`
разом з `agent/workflowagent`). Кожен приклад — окремий пакет `main`, який будує
`workflowagent` з графа вузлів і віддає його консольному лаунчеру.

Одинадцять прикладів графового рушія (плюс шістнадцять тижневих — див. нижче), з них сім працюють **без ключа й без мережі**. Кожен має
власний `README.md` зі схемою Mermaid, метою, командою запуску та прикладом сесії.

## Швидкий старт

```bash
cd demo/4_adk_examples/basic
go run . console
```

З теки цієї збірки (власний `Taskfile.yml`, як у `demo/3_adk2_patterns`) у
кожного прикладу є **власна задача**, названа за його шляхом: `/` і `_`
замінюються на `-`. Тож `routing/llm` → `task routing-llm`, а
`dynamic/use_as_output` → `task dynamic-use-as-output`:

```bash
task list                          # перелік усіх задач і прикладів
task basic                         # приклад із теки basic/
task dynamic-llm                   # приклад із теки dynamic/llm/
task routing-int ARGS=-input       # свої аргументи замість console
task all                           # прогнати всі 11 прикладів поспіль
task build                         # зібрати всі (артефактів не лишає)
task verify                        # fmt + vet + build
```

Кожна така задача — це просто `go run ./<тека> {{.ARGS}}`, виписаний явно, тож
рядок із неї працює в терміналі як є. Задача `run P=<шлях>` лишаємо для
довільного шляху: `task run P=basic`.

### `task all`

Проганяє **кожен** приклад поспіль як смоук-запуск: подає `EOF` на stdin, тож
кожен приклад стартує, друкує свій слід і виходить, і прогін не залипає в
інтерактивній консолі. Приклади, яким потрібна модель, а провайдера не
налаштовано, позначаються `❌`, решта — `✅`; підсумок друкує, скільки впало.
Задача повертає **ненульовий код**, якщо хоч один приклад не запустився, тож
`task all` у CI не виглядає зеленим, коли половина каталогу не стартує.

На машині без ключів очікуйте рівно чотири `❌` — `complex`, `dynamic/llm`,
`dynamic/use_as_output`, `routing/llm`. Щоб прогнати їх, налаштуйте провайдера
(див. нижче).

Задачі, що вимагають лише одного прикладу, названі за його шляхом: `/` і `_`
замінюються на `-`. Тож `routing/llm` → `task routing-llm`, а
`dynamic/use_as_output` → `task dynamic-use-as-output`.

### Web UI

Кожен приклад запускається і як вебсервер — тими самими трьома аргументами
лаунчера, що й решта демо курсу:

```bash
task basic ARGS="web webui api"
```

Три аргументи — це три різні речі: `web` піднімає сервер, `webui` віддає
сторінку на `/ui/`, `api` відкриває REST на `/api`. Після запуску:

- http://localhost:8080/ui/ — інтерфейс
- http://localhost:8080/api — REST API

Усі одинадцять перевірено наживо в цьому режимі: `/api/list-apps` віддає ім'я
застосунку, `/ui/` повертає `200`, і хід справді виконується через
`/api/run_sse` — `basic` повертає `HELLO WEB`, а `routing/llm` і класифікацію,
і відповідь на запит.

**Усі одинадцять слухають `:8080`**, тож одночасно працює лише один. Ctrl-C
часто лишає дочірній процес живим, і наступний запуск падає з
`address already in use` — ліки ті самі, що й для решти демо:

```bash
task kill:8080       # звільнити порт
```

Задачі `all:web` немає навмисно: вона піднімала б одинадцять серверів на
одному порту. Для прогону «всього поспіль» є `task all` — він запускає
приклади як консольні смоук-запуски.

З кореня репозиторію той самий Web UI доступний як `demo:4_adk:*` — по задачі на
кожен приклад плюс узагальнена:

```bash
task demo:4_adk:basic:web          # Web UI для basic/
task demo:4_adk:routing:llm:web     # Web UI для routing/llm/
task demo:4_adk:web P=routing/llm   # Web UI для довільного шляху
```

Коренева поверхня навмисно лише з `:web`-задачами: щоб подивитися приклад,
досить відкрити Web UI. Голі консольні входи (`task basic`, `task all`,
`task run P=...`) живуть у `Taskfile.yml` цієї теки.

## Провайдер і ключі

Приклади, яким потрібна модель, читають провайдера так само, як лабораторні
дня 3: ключі беруться з `apps/.env` (шаблон — `apps/.env-example`), а
`MODEL` / `DEFAULT_MODEL_PROVIDER` називають провайдера й модель. Рішення ухвалює
`internal/modelcfg` і друкує його у stderr, тож хибну відповідь видно одразу.
Явно експортована змінна завжди перемагає файл.

`DEFAULT_MODEL_PROVIDER` можна лишити порожнім — тоді провайдера визначить
наявний ключ (порядок: agentgateway → ollama → gemini → openai). Секрети в
README не записуйте.

`complex` потребує **саме Gemini** (провайдер `gemini` або `agentgateway/gemini`),
бо дослідники використовують Google Search grounding.

## Приклади

| Приклад | Тема | Що показує | LLM? |
|---|---|---|---|
| [`basic`](./basic) | Основи | послідовний ланцюжок `FunctionNode` через `workflow.Chain` | Ні |
| [`complex`](./complex) | Паралельність | розгалуження → зведення в `JoinNode` → синтез моделлю | Так |
| [`routing/string`](./routing/string) | Маршрутизація | гілка за рядковим значенням через `StringRoute` | Ні |
| [`routing/int`](./routing/int) | Маршрутизація | гілка за числом через `IntRoute` / `MultiRoute[int]` | Ні |
| [`routing/llm`](./routing/llm) | Маршрутизація | класифікує модель, маршрут ставить рушій | Так |
| [`hitl_simple`](./hitl_simple) | HITL | мінімальна пауза/продовження на двох вузлах | Ні |
| [`hitl_rerun`](./hitl_rerun) | HITL | повторний вхід в один вузол через `ResumeOrRequestInput` | Ні |
| [`dynamic/basic`](./dynamic/basic) | Динаміка | імперативна оркестрація дочірніх вузлів | Ні |
| [`dynamic/hitl`](./dynamic/hitl) | Динаміка | динамічний оркестратор, що зупиняється на ввід людини | Ні |
| [`dynamic/llm`](./dynamic/llm) | Динаміка | динамічний оркестратор викликає вузол `LlmAgent` | Так |
| [`dynamic/use_as_output`](./dynamic/use_as_output) | Динаміка | виведення дитини стає виведенням оркестратора | Так |

## Тижневі приклади (`week1` … `week6`)

Окрім одинадцяти прикладів графового рушія, у каталозі лежать шістнадцять
прикладів-супутників до тижнів курсу: `weekN/partM/cmd`. Це окремі пакети
`main` кореневого модуля (власного `go.mod` немає), які використовують
спільні `internal/adkrun`, `internal/modelcfg`, `internal/scripted` і
`internal/usage`.

| Приклад | Тема | Без ключа? | Задача |
|---|---|---|---|
| [`week1/part1`](./week1/part1/cmd) | ModelBenchmarkAgent — порівняння моделей за затримкою, ціною, якістю | Так | `task week1-part1` |
| [`week1/part2`](./week1/part2/cmd) | StrictSchemaEnforcer — типовий контракт і JSON-схема | Так | `task week1-part2` |
| [`week2/part1`](./week2/part1/cmd) | WorkflowGraphAgent — явний граф із типізованими вузлами | Так | `task week2-part1` |
| [`week2/part2`](./week2/part2/cmd) | AgentService — агент за HTTP, health, graceful shutdown | Так, **сервер :8080** | `task week2-part2` |
| [`week3/part1`](./week3/part1/cmd) | OntologyAwareKnowledgeBuilder — PDF → чанки → сутності | Так | `task week3-part1` |
| [`week3/part2`](./week3/part2/cmd) | AgenticGraphRAGEngine — планувальник, гібридний пошук, реранк | Так | `task week3-part2` |
| [`week4/part1`](./week4/part1/cmd) | ReAct-цикл, написаний вручну | `-offline` | `task week4-part1` |
| [`week4/part2`](./week4/part2/cmd) | відмовостійкий harness інструментів | `-offline` | `task week4-part2` |
| [`week5/part1`](./week5/part1/cmd) | пам’ять агента як event-sourced стан | `-offline` | `task week5-part1` |
| [`week5/part2`](./week5/part2/cmd) | координатор, desks, людське схвалення | `-offline` | `task week5-part2` |
| [`week6/part1`](./week6/part1/cmd) | динамічний workflow maker/gate/critics | `-offline` | `task week6-part1` |
| [`week6/part2`](./week6/part2/cmd) | самополіпшення за autoresearch, із гальмами | `-offline` | `task week6-part2` |
| [`week6/part3`](./week6/part3/cmd) | панель агентів із вузьким каналом | Так | `task week6-part3` |
| [`week6/part4`](./week6/part4/cmd) | A2A: виклик агента через loopback-сокет | Так | `task week6-part4` |
| [`week6/part5`](./week6/part5/cmd) | agentic SDLC: координатор і воркери | Так | `task week6-part5` |
| [`week6/part6`](./week6/part6) | harness як kagent BYO-агент ([runbook](./week6/part6/README.md)) | `-offline`, **сервер :8080** | `task week6-part6` |

```bash
task week4-part2                              # за замовчуванням -offline
task week4-part2 WA="-offline -faults 503,timeout"
task week4-part1 WA=""                        # справжня модель (apps/.env)
task weeks:list                               # перелік тижневих прикладів
task weeks:all                                # смоук усіх, крім двох серверів
```

`WA` — це аргументи **самого прикладу** (`-offline`, `-query`, `-faults`, …), а
не лаунчера, тому окремо від `ARGS`. Для тижнів 4–6 типове `WA` — `-offline`:
скриптована модель замість провайдера, детермінований вивід. Порожній `WA`
вмикає справжню модель через `internal/modelcfg`; перший рядок виводу називає
мозок. Ці приклади **не входять** у `task list` і `task all` з розділів вище:
ті шукають `main.go` на глибині ≤3, а тижневі лежать глибше.

`week2/part2` і `week6/part6` — сервери на `:8080`, самі не завершуються й
виключені з `weeks:all`; порт звільняє `task kill:8080` з кореня.

## Базові поняття

- **Вузли** — одиниці роботи: `FunctionNode` (Go-функція), `AgentNode` (обгортка
  над `LlmAgent`), `ToolNode` (обгортка над `tool.Tool`), `JoinNode` (бар'єр
  зведення) і `DynamicNode` (імперативний оркестратор).
- **Ребра** — задаються через `workflow.Chain`, `workflow.Concat` і `EdgeBuilder`
  (`Add`, `AddFanOut`, `AddFanIn`).
- **Маршрутизація** — вузол ставить `Event.Routes`; відповідні ребра
  використовують `StringRoute`, `IntRoute` або `MultiRoute`.
- **HITL** — вузол породжує `RequestInput`, щоб зупинити виконання; запуск
  продовжується, коли людина відповість.

---

Станом на 10/2026 · `google.golang.org/adk/v2 v2.5.0` · Go 1.27.1
