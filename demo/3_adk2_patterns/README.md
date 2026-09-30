# 3 · Каталог патернів ADK 2.x — запускні приклади на Go

Шістнадцять патернів агентних систем з
[каталогу ADK 2.x](../../docs/adk/adk-2x-pattern-catalog-ua.html) плюс два
додатки. Кожен патерн — окрема тека з власним `main.go`, тестом і `README.md`.
Усі приклади запускаються **без ключа й без мережі**, і кожен можна
перемкнути на реальну модель прапорцем `-live`.

## Швидкий старт

```bash
cd demo/3_adk2_patterns/b4_conditional_route
go run .                      # сценарне демо, офлайн
go run . -input "..."         # власний вхід
go run . -live                # модель з apps/.env (MODEL / DEFAULT_MODEL_PROVIDER)
go run . console              # інтерактивно (офлайн-«мозок» відповідає на будь-який ввід)
go run . web api webui        # ADK Web UI на http://localhost:8080/ui/
```

З теки цієї лаби (власний `Taskfile.yml`, як у `demo/adk-quickstart`):

```bash
task list                                  # перелік тек
task run P=b4_conditional_route            # один патерн
task run P=e1_human_in_the_loop ARGS=console
task all                                   # усі демо поспіль
task test                                  # тести, race detector, офлайн
task cover                                 # гейт 85% на кожен пакет
task verify                                # fmt + vet + test + cover
```

Окремого `go.mod` тут немає: пакети належать кореневому модулю, тому `task`
працює з теки лаби, а `go test ./...` резолвиться через кореневий модуль.

## Каталог

Відсортовано за віссю «хто вирішує наступний крок»: що нижче в таблиці
категорій C, то більше викликів моделі йде на рішення, а не на роботу.
Правило руху одне: беріть патерн правіше лише тоді, коли лівий **виміряно**
не впорався.

| # | Патерн | Хто вирішує | Тека | Ключові примітиви Go |
|---|---|---|---|---|
| A1 | Single Agent | модель | [a1_single_agent](a1_single_agent/) | `llmagent.New`, `functiontool.New` |
| A2 | ReAct | модель | [a2_react](a2_react/) | `NewDynamicNode` + `RunNode`, кап кроків |
| B1 | Sequential Pipeline | код | [b1_sequential_pipeline](b1_sequential_pipeline/) | `workflow.Chain`, `NewAgentNode` |
| B2 | Parallel Fan-Out / Gather | код | [b2_fan_out_gather](b2_fan_out_gather/) | `AddFanOut`, `AddFanIn`, `NewJoinNode` |
| B3 | Loop | код | [b3_loop](b3_loop/) | маршрут на зворотному ребрі, `ErrUnconditionalCycle` |
| B4 | Conditional Route | код | [b4_conditional_route](b4_conditional_route/) | `AddRoutes`, `ev.Routes`, `Default` |
| B5 | Custom Logic | код | [b5_custom_logic](b5_custom_logic/) | `NewDynamicNode`, `RunNode`, `WithRunID` |
| C1 | Coordinator / Dispatcher | модель | [c1_coordinator](c1_coordinator/) | `SubAgents` + `ModeSingleTurn` |
| C2 | Hierarchical Decomposition | модель | [c2_hierarchical_decomposition](c2_hierarchical_decomposition/) | `agenttool.New` у два рівні |
| C3 | Swarm | модель | [c3_swarm](c3_swarm/) | примітива немає: `NewDynamicNode` + «дошка» |
| C4 | Routed Agent | код | [c4_routed_agent](c4_routed_agent/) | у Go відсутній: власний роутер + failover |
| D1 | Review and Critique | код | [d1_review_critique](d1_review_critique/) | генератор → критик → `BoolRoute` |
| D2 | Iterative Refinement | код | [d2_iterative_refinement](d2_iterative_refinement/) | цикл, детермінований перевіряч, найкращий ≠ останній |
| E1 | Human-in-the-Loop | людина | [e1_human_in_the_loop](e1_human_in_the_loop/) | `ResumeOrRequestInput`, `RerunOnResume` |
| E2 | Tool Confirmation Gate | людина | [e2_tool_confirmation_gate](e2_tool_confirmation_gate/) | `RequireConfirmationProvider` |
| E3 | Ambient Agent | код | [e3_ambient_agent](e3_ambient_agent/) | черга подій, сесія на подію |
| X | Function Graph (без LLM) | код | [x_function_graph](x_function_graph/) | лише `NewFunctionNode`: ланцюг, віяло, маршрут, цикл |
| X | Guards (анти-патерни) | валідатор | [x_guards](x_guards/) | помилки збирання графа, які Go ловить сам |

## Як це влаштовано

```
demo/3_adk2_patterns/
├── internal/kit/          спільний харнес (не патерн)
│   ├── brain.go           офлайн-модель: Brain вирішує відповідь за станом запиту
│   ├── run.go             kit.Run — прогін у пам'яті, слід подій, відповіді на HITL-паузи
│   └── main.go            kit.Main — прапорці -live / -input і передача решти в ADK launcher
└── <патерн>/
    ├── main.go            var spec = kit.Spec{…}; func build(kit.Models) (agent.Agent, error)
    ├── main_test.go       табличні тести, офлайн
    └── README.md          схема, запуск, примітиви, коли брати / не брати
```

**Чому офлайн-модель — це правила, а не записаний сценарій.** Демо запускають
і в `console`, де вхід — будь-що. `kit.Brain` дивиться на запит (текст
користувача, які інструменти вже повернули результат) і вирішує відповідь.
Тому той самий агент відповідає на довільний ввід детерміновано й без ключа.
У режимі `-live` `Brain` не використовується: агента веде його `Instruction`.

**Легенда сліду:** `👤` вхід, `🤖` текст моделі, `🔧` виклик інструмента,
`📦` результат інструмента, `⚙️` вихід вузла графа, `🔀` маршрут,
`🙋` пауза на людину, `⏸` запуск припарковано.

## Перевірка

```bash
task test      # go test -race ./... — офлайн, без ключа
task cover     # гейт 85% на кожен пакет (AGENTS.md §4)
```

Ті самі команди без `task` — з кореня репозиторію:

```bash
go test -race ./demo/3_adk2_patterns/...
sh ./scripts/covgate.sh 85 demo/3_adk2_patterns
```

## Суміжне

- [docs/adk/adk-2x-pattern-catalog-ua.html](../../docs/adk/adk-2x-pattern-catalog-ua.html) — теорія: вісь, дерево рішень, анти-патерни
- [docs/adk/adk-go-workflows.html](../../docs/adk/adk-go-workflows.html) — API графів
- [.agents/skills/adk-go-workflow/](../../.agents/skills/adk-go-workflow/) — скіл для AI-агентів з довідкою по цих патернах

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.4.0` · Go 1.27.1
