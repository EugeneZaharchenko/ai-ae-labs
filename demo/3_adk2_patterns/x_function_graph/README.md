# X · Function Graph (без LLM)

**Хто вирішує наступний крок:** код · **Категорія:** додаток · граф лише з функційних вузлів

Увесь робочий процес зібрано з `workflow.NewFunctionNode`. У графі немає
жодного `LlmAgent`, моделі чи ключа. Рушій графа сам дає порядок кроків,
паралельні гілки, бар'єр злиття, типізовані маршрути й обмежений цикл.
Це найлівіша точка осі «код ↔ модель»: спершу перевірте, чи задачу не
розв'язує такий граф, і лише потім додавайте агентів.

```
Start → parse_order ─┬→ price_check ─┐
                     ├→ stock_check ─┼→ checks (Join) → decide ─approve─→ reserve ⟲ backoff
                     └→ fraud_check ─┘                        │           ├done────→ ship
                                                              │           └give_up─→ backorder
                                                              ├review──→ manual_review
                                                              └Default─→ reject
```

## Запуск

```bash
go run .                                    # approve → reserve успішний з 3-ї спроби → ship
go run . -input "order 7: 2x gpu 1800"      # fraud-ліміт → manual_review
go run . -input "order 9: 1x unicorn 10"    # немає на складі → reject (Default)
go run . -input "order 5: 2x mouse 10"      # замок не звільняється → backorder після капу
go run . console                            # формат вводу: order <id>: <к-сть>x <товар> <ціна>
```

`-live` тут нічого не змінює: у графі немає вузла, якому потрібна модель.

Очікуваний слід офлайн-демо (скорочено):

```
⚙️  parse_order ⇒ {"id":"1001","qty":3,"item":"keyboard","price":120}
⚙️  fraud_check ⇒ {...}            ← три перевірки йдуть паралельно,
⚙️  price_check ⇒ {...}              порядок рядків може відрізнятися
⚙️  stock_check ⇒ {...}
⚙️  checks ⇒ {"fraud_check":{...},"price_check":{...},"stock_check":{...}}
🔀 decide route=[approve]
🔀 reserve route=[retry]
🔀 reserve route=[retry]
🔀 reserve route=[done]
⚙️  ship ⇒ shipped: order 1001 (3x keyboard) after 3 reserve attempt(s)
```

## Go-примітиви (ADK Go v2.5.0)

| Примітив | Роль у демо |
|---|---|
| `workflow.NewFunctionNode(name, fn, cfg)` | кожен крок — звичайна Go-функція з типізованим входом і виходом |
| `EdgeBuilder.AddFanOut / AddFanIn` | три незалежні перевірки паралельно |
| `workflow.NewJoinNode("checks")` | бар'єр; вихід — `map[ім'я попередника]вихід` |
| функція, що повертає `*session.Event` з `ev.Routes` | `decide` і `reserve` обирають гілку |
| `workflow.StringRoute(...)`, `workflow.Default` | типізовані ребра та запасна гілка |
| ребро `backoff → reserve` + маршрут `retry` | цикл; без маршруту граф впав би з `ErrUnconditionalCycle` |

## Коли брати

Порядок кроків відомий, рішення про гілку робить правило, а не судження.
Валідація, ETL, обробка замовлень, маршрутизація за полем. Такий граф
безкоштовний у експлуатації, детермінований і тестується без мережі.

## Коли не брати

Крок потребує розуміння вільного тексту, а не правила. Тоді один вузол стає
агентом ([B4](../b4_conditional_route/) — класифікатор + той самий диспетч,
[B1](../b1_sequential_pipeline/) — агент усередині ланцюга).

## Що подивитися в коді

- **Join несе лише виходи своїх попередників.** Тому кожен `check` містить
  `Order`: крок після бар'єра не може «зазирнути» назад до `parse_order`.
- **Кап циклу — ваш код.** Валідатор гарантує лише, що зворотне ребро має
  маршрут. `maxReserveAttempts` зупиняє цикл, якщо замок так і не звільнився
  (`mouse` → `backorder`).
- **Невідомий вердикт не ставить маршруту.** Тоді спрацьовує лише `Default` —
  так відмова лишається явною гілкою, а не тихим кінцем графа.
- **Помилка вузла зупиняє запуск.** Неправильний ввід падає в `parse_order`
  з повідомленням про очікуваний формат (`TestBadInputFailsTheRun`).

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.5.0`
