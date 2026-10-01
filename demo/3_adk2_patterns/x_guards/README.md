# X · Запобіжники (анти-патерни, які ловить збирання графа)

**Хто вирішує наступний крок:** валідатор графа · **Категорія:** анти-патерни (модуль 09 каталогу)

Демо навмисно збирає зламані графи й друкує помилку, яку повертає
`workflow.New`. Усі ці перевірки спрацьовують **до** першого виклику моделі —
тобто до того, як щось коштуватиме грошей. Останні рядки — відмови, яких не
ловить жоден валідатор: це ваша дисципліна.

```
зламаний граф → workflow.New(name, edges) → помилка збирання  (🛡️ спрацювало / ⚠️ ні)
правильний граф (build): Start → fan-out(research_a, research_b) → JoinNode → merge
```

## Запуск

```bash
go run .                     # таблиця запобіжників, без ключа
go run . console             # правильний граф: fan-out → JoinNode → merge
go run . web api webui       # Web UI на http://localhost:8080/ui/
```

Очікуваний слід офлайн-демо:

```
🛡️  loop with no exit condition               → unconditional cycle detected: "poll"
🛡️  merging branches without a JoinNode       → non-JoinNode fan-in is not yet supported: node "merge" …
🛡️  two Default branches on one router        → node has more than one default route: "router"
🛡️  node nobody can reach                     → nodes not reachable from start node: "orphan, orphan_next"
🛡️  two different nodes with one name         → duplicate node name: step
🛡️  Task-mode agent as a static graph node    → Agent "helper" has mode='task' and cannot be used as a workflow graph node
🛡️  chat-mode agent fed by a predecessor node → Agent "helper" has mode='chat' and cannot follow node "prepare": …
not caught by any validator — your discipline:
   · loop WITH a route but without an iteration cap → …
```

## Go-примітиви (ADK Go v2.5.0)

| Запобіжник | Анти-патерн | Як це виглядає в Go |
|---|---|---|
| `workflow.ErrUnconditionalCycle` | цикл без умови виходу | жодне ребро циклу не несе `Route` |
| `workflow.ErrUnsupportedFanIn` | злиття гілок без `JoinNode` | >1 безумовної вхідної дуги у не-`*JoinNode` |
| `workflow.ErrMultipleDefaultRoutes` | два `Default` на одному вузлі | `AddRoute(r, x, workflow.Default)` двічі |
| `workflow.ErrNodesNotReachable` | вузол, до якого не дійти від `Start` | «осиротілий» ланцюжок ребер |
| `workflow.ErrDuplicateNodeName` | два різні вузли з одним іменем | `Chain(Start, step("step"), step("step"))` |
| `validateNoTaskModeGraphNodes` (без sentinel) | Task-агент статичним вузлом графа | `llmagent.Config.Mode = ModeTask` у `NewAgentNode` |
| `validateChatModeWiring` (без sentinel) | chat-агент після іншого вузла | `ModeChat` не бачить вхід попередника |

Дві перевірки режимів не мають експортованого sentinel-а, тому тест звіряє текст
помилки (`mode='task'`, `mode='chat'`), а решту — через `errors.Is`.

## Коли брати

Прочитайте цю таблицю **до** того, як писати граф: знати запобіжники наперед
дешевше, ніж зустріти їх у логах.

## Коли не брати

Це не патерн для продакшну, а карта. Вона не замінює ліміт ітерацій, ліміт
паралельності, якість критика й порядок побічних ефектів — для них механічного
запобіжника немає.

## Що подивитися в коді

- `cases()` — кожен випадок складається з кількох рядків; порівняйте з
  правильною версією в `build`.
- `uncaught` — список того, що валідатор **не** ловить: цикл із маршрутом, але
  без лічильника; fan-out без ліміту; розмитий критик; побічний ефект до паузи
  HITL.
- `build` — `workflowagent.New` у v2.5.0 викликає `workflow.New` **без опцій**,
  тож `workflow.WithMaxConcurrency(n)` не дістається до графа, зібраного через
  нього. Усередині `workflowagent` обмежуйте паралельність через
  `workflow.NewParallelWorker(name, node, maxConcurrency, cfg)`.

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.5.0`
