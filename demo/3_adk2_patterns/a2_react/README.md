# A2 · ReAct

**Хто вирішує наступний крок:** модель · **Категорія:** A · один цикл міркування

Цикл «думка → дія → спостереження», доки не спрацює умова виходу. Типу з назвою
ReAct в ADK немає — це форма графа. Тут це динамічний вузол зі звичайним
Go-циклом. Жорсткий ліміт кроків (`maxSteps = 4`) пишемо ми: фреймворк його не дає.

```
Start → react (DynamicNode)
          цикл ≤ maxSteps:
            planner (LlmAgent)     → "SEARCH: q" | "FINAL: answer"
            search  (FunctionNode) → спостереження
```

## Запуск

```bash
go run .                                        # сценарне демо, без ключа
go run . -input "How does the graph engine work?"   # відповідь з першого пошуку
go run . -live                                  # той самий цикл на моделі з apps/.env
go run . console                                # діалог у консолі
go run . web api webui                          # Web UI на http://localhost:8080/ui/
```

Очікуваний слід офлайн-демо: перший запит повертає порожньо, планувальник
перепланує, другий запит знаходить відповідь.

```
🤖 planner: SEARCH: When was ADK Go 2.0 announced?
⚙️  search ⇒ (no results)
🤖 planner: SEARCH: When was ADK Go 2.0 announced release date
⚙️  search ⇒ ADK Go 2.0 was announced in 2026 with a graph workflow engine.
🤖 planner: FINAL: ADK Go 2.0 was announced in 2026 with a graph workflow engine.
⚙️  react ⇒ {"answer":"ADK Go 2.0 was announced in 2026 …","steps":3}
```

## Go-примітиви (ADK Go v2.4.0)

| Примітив | Роль у демо |
|---|---|
| `workflow.NewDynamicNode[IN,OUT](name, fn, cfg)` | тіло циклу — звичайний Go-код; повертає `workflow.Node` |
| `workflow.RunNode[OUT](ctx, child, input)` | один крок: думка (planner) або дія (search); кожен виклик — окрема кешована активація |
| `workflow.NewAgentNode(llmagent, cfg)` | planner як вузол; single-turn бачить лише свій вхід |
| `workflow.NewFunctionNode` | інструмент пошуку як детермінований вузол |

## Коли брати

Середовище відповідає непередбачувано: пошук нічого не знайшов, шлях
заблоковано. ReAct робить перепланування явним, а слід міркувань — найкращий
артефакт для дебагу.

## Коли не брати

Статичні задачі з відомим порядком кроків і шляхи, критичні до затримки. Це
приблизно втричі більше викликів моделі на ту саму задачу, ніж
[A1](../a1_single_agent/). Без жорсткого ліміту кроків цикл блукає.

## Що подивитися в коді

- `maxSteps` і гілка `Capped` — вихід за бюджетом повертає чесне «не знайшов», а
  не помилку і не вигадану відповідь.
- `scratchpad` — контекст планувальника на кожному кроці: питання і всі пари
  Action/Observation. Історію несе цикл, а не сесія.
- Порушення протоколу (відповідь без `SEARCH:`/`FINAL:`) записується як
  спостереження, і цикл іде далі — тест `protocol break` це перевіряє.

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.4.0`
