# B3 · Loop

**Хто вирішує наступний крок:** код · **Категорія:** B · детермінована оркестрація

Повторювати кроки, доки не пройде **відомий** тест виходу. Ребро назад — це ребро
графа з маршрутом; ліміт ітерацій (`maxPolls = 5`) пишете ви.

```
Start → submit → poll ─BoolRoute(true)──→ report (LlmAgent)
                  ↑  └─BoolRoute(false)─→ wait ─┐
                  └──────── Default ────────────┘
```

## Запуск

```bash
go run .                                   # задача завершується на 3-му опитуванні
go run . -input "render video, needs 9"    # ліміт зупиняє задачу, що не завершується
go run . -live                             # report на моделі з apps/.env
go run . console                           # діалог у консолі
go run . web api webui                     # Web UI на http://localhost:8080/ui/
```

Очікуваний слід офлайн-демо:

```
⚙️  submit ⇒ {"id":"job-1","needs":3,"polls":0,"status":"running"}
⚙️  poll ⇒ {"id":"job-1","needs":3,"polls":1,"status":"running"}
🔀 poll route=[false]
⚙️  wait ⇒ {…"polls":1…}
⚙️  poll ⇒ {…"polls":2…}
🔀 poll route=[false]
⚙️  wait ⇒ {…"polls":2…}
⚙️  poll ⇒ {"id":"job-1","needs":3,"polls":3,"status":"done"}
🔀 poll route=[true]
🤖 report: Your job finished. (…)
```

## Go-примітиви (ADK Go v2.5.0)

| Примітив | Роль у демо |
|---|---|
| `EdgeBuilder.AddRoute(from, to, route)` | вихід (`true`) і нове коло (`false`) |
| `workflow.BoolRoute` | тип маршруту; значення в `Event.Routes` — `"true"`/`"false"` |
| `workflow.Default` | ребро назад `wait → poll`; рахується умовним |
| `session.Event.Routes` | `poll` повертає `*session.Event` і сам ставить маршрут |
| `workflow.ErrUnconditionalCycle` | запобіжник на етапі збирання графа |

Альтернатива без графа — `loopagent.New(loopagent.Config{MaxIterations: n})` +
`tool/exitlooptool`.

## Коли брати

Опитувати задачу, доки вона не завершиться; крутити цикл «компіляція →
виправлення», доки збірка не позеленіє. Кількість повторів невідома, а тест
виходу — відомий.

## Коли не брати

Коли надійну умову виходу написати не можна. Якщо «готово» — це судження, а не
тест, потрібен D2 із жорстким лімітом, а не відкритий цикл.

## Що подивитися в коді

- **ADK ловить половину помилки.** Цикл, у якому всі ребра безумовні, не
  збереться: `unconditionalCycle()` повертає `workflow.ErrUnconditionalCycle`
  (тест `TestUnconditionalCycleRejected`). Цикл із маршрутом, але без лічильника
  збереться — друга половина запобіжника це `maxPolls`.
- **Чому ребро назад — `Default`, а не `Add`.** У `poll` вже входить безумовне
  ребро від `submit`. Друге безумовне вхідне ребро — це злиття без `JoinNode`,
  і граф падає з `ErrUnsupportedFanIn`. `Default` рахується умовним, тож такий
  цикл легальний.
- Ліміт не замінює тест виходу: `status` каже, чи задача справді завершилась
  (`done`), чи ми просто перестали питати (`gave up: poll cap reached`).

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.5.0`
