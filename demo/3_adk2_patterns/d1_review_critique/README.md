# D1 · Review and Critique

**Хто вирішує наступний крок:** код · **Категорія:** D · якість, куплена за додаткові виклики

Генератор видає результат. Окремий критик у **власному** контексті перевіряє його
за переліченими критеріями й повертає структурований вердикт. Далі код, а не
модель, обирає гілку: опублікувати або повернути на доопрацювання.

```
Start → generate (LlmAgent, OutputKey=draft) → critic (LlmAgent) → gate ─true──→ publish
                                                                      └false─→ send_back
```

## Запуск

```bash
go run .                                            # шлях «схвалено», без ключа
go run . -input "Promise ORD-42 arrives tomorrow"    # шлях «на доопрацювання»
go run . -live                                      # генератор і критик на моделі з apps/.env
go run . console                                    # діалог у консолі
go run . web api webui                              # Web UI на http://localhost:8080/ui/
```

Очікуваний слід офлайн-демо:

```
🤖 generate: Sorry, order ORD-42 is delayed; the new ETA is 2026-10-01.
🤖 critic: {"approve":true}
🔀 gate route=[true]
⚙️  publish ⇒ published: Sorry, order ORD-42 is delayed; the new ETA is 2026-10-01.
```

На завідомо поганому вході (`-input "Promise ORD-42 arrives tomorrow"`):

```
🤖 generate: We guarantee order ORD-42 arrives tomorrow, promise!
🤖 critic: {"approve":false,"failed":["C3"]}
🔀 gate route=[false]
⚙️  send_back ⇒ sent back to the writer, failed C3: …
```

## Go-примітиви (ADK Go v2.5.0)

| Примітив | Роль у демо |
|---|---|
| `workflow.NewAgentNode(llmagent, cfg)` | генератор і критик як вузли графа (режим single-turn за замовчуванням) |
| `llmagent.Config.OutputKey` | чернетка потрапляє в стан сесії під ключем `draft` |
| `workflow.BoolRoute` + `EdgeBuilder.AddRoute` | дві гілки вердикту: `true` → publish, `false` → send_back |
| `session.Event.Routes` | вузол `gate` ставить маршрут `"true"`/`"false"` (рядкова форма `BoolRoute`) |
| `agent.Context.State()` | `publish` / `send_back` читають чернетку зі стану |

Іменованого примітива «critic» немає в жодній мові: це два вузли й один
маршрут між ними.

## Коли брати

Результат має пройти планку. Це найвища якість на витрачений токен у всьому
каталозі: другий агент з іншим промптом ловить те, що імпульс генератора проніс
повз.

## Коли не брати

Інтерактивні шляхи, чутливі до затримки, і низькоставковий вивід, де дефект
коштує менше за перевірку. Якщо один прохід критики замало й є вимірюваний
сигнал якості — [D2](../d2_iterative_refinement/).

## Що подивитися в коді

- `criteria` — перелічені критерії C1–C3. «Переглянь це» породжує підлабузництво;
  список перевірюваних пунктів — ні.
- `parseVerdict` — вердикт, який не вдалося розібрати, означає **reject**, а не
  тихе схвалення.
- `TestReviewCritique` перевіряє критика на завідомо поганих виходах: критик,
  який ніколи нічого не відхилив, — це не якість, а рядок у рахунку.
- `review` — детермінована перевірка тих самих критеріїв. Офлайн-критик
  використовує її; у режимі `-live` той самий список іде в `Instruction`.

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.5.0`
