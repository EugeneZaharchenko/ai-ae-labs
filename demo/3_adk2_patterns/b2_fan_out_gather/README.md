# B2 · Parallel Fan-Out / Gather

**Хто вирішує наступний крок:** код · **Категорія:** B · детермінована оркестрація

Незалежні підзадачі виконуються одночасно, а їхні виходи зливаються в одному
місці — `JoinNode`. Потім модель узгоджує гілки, які можуть суперечити одна одній.

```
                ┌→ pricing ─────┐
Start → topic ──┼→ reviews ─────┼→ gather (JoinNode) → merge (fn) → verdict (LlmAgent)
                └→ competitors ─┘
```

## Запуск

```bash
go run .                                   # сценарне демо, без ключа
go run . -input "Should we buy the Volt V2?"
go run . -live                             # verdict на моделі з apps/.env
go run . console                           # діалог у консолі
go run . web api webui                     # Web UI на http://localhost:8080/ui/
```

Очікуваний слід офлайн-демо:

```
⚙️  topic ⇒ Acme X1 e-bike
⚙️  pricing ⇒ Acme X1 e-bike: 1 890 EUR, 12% above segment median
⚙️  reviews ⇒ Acme X1 e-bike: 4.6/5 from 1 200 owners; battery praised
⚙️  competitors ⇒ Acme X1 e-bike: a cheaper alternative exists (Volt V2, 1 490 EUR)
⚙️  gather ⇒ {"competitors":"…","pricing":"…","reviews":"…"}
🤖 verdict: VERDICT: buy — strong reviews outweigh the price; …
```

## Go-примітиви (ADK Go v2.4.0)

| Примітив | Роль у демо |
|---|---|
| `EdgeBuilder.AddFanOut(from, to…)` | одне ребро в кожну гілку |
| `EdgeBuilder.AddFanIn(to, from…)` | усі гілки → `gather` |
| `workflow.NewJoinNode(name)` | бар'єр: чекає всіх; вихід — `map[string]any` за іменами гілок |
| `workflow.New(name, edges, workflow.WithMaxConcurrency(n))` | ліміт одночасних гілок (`maxConcurrency = 2`) |
| `agent.New(agent.Config{Run: w.Run})` | обгортка графа в агента — див. нижче |

**Чому не `workflowagent.New`.** У v2.4.0 `workflowagent.Config` не має поля для
ліміту паралельності: `WithMaxConcurrency` — це опція `workflow.New`. Тому граф
збирається через `workflow.New(…, WithMaxConcurrency(2))`, а `Workflow.Run` вже
має сигнатуру `agent.Config.Run`. Ціна: така обгортка не відновлює HITL-паузи
(`workflowagent` робить це в `detectResume`), тож для графа з людиною в петлі
цей шлях не підходить. Для одного вузла є ще `workflow.NewParallelWorker(name,
wrapped, maxConcurrency, cfg)`.

## Коли брати

Три звернення до API, які не залежать одне від одного, не мають коштувати три
послідовні round-trip-и. Варіант «голосування» — те саме питання кільком
агентам — купує впевненість, якої один виклик не дає.

## Коли не брати

Залежні підзадачі (це [B1](../b1_sequential_pipeline/)) і розгортання, обмежені
за вартістю. Найважче тут не розпаралелити, а злити: гілки суперечать одна одній,
і хтось має вирішити, хто правий (тут — `verdict`).

## Що подивитися в коді

- Два запобіжники, і лише один механічний. Злиття без `JoinNode` не збереться:
  тест `TestFanInNeedsJoinNode` отримує `workflow.ErrUnsupportedFanIn`. Ліміт
  паралельності — ваша дисципліна: без `WithMaxConcurrency` усі гілки стартують
  разом.
- `gauge` рахує пік одночасних гілок; `TestFanOut` перевіряє, що з лімітом 2 пік
  дорівнює 2, з лімітом 1 — 1, без ліміту — 3.
- `merge` сортує ключі мапи з `JoinNode`: порядок завершення гілок
  недетермінований, а текст для моделі має бути стабільним.

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.4.0`
