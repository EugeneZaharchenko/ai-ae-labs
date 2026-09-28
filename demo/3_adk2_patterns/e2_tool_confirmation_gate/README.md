# E2 · Tool Confirmation Gate

**Хто вирішує наступний крок:** людина · **Категорія:** E · людина й тригер

Агент планує й читає вільно. Схвалення людини потребує лише одна незворотна
дія — конкретний виклик інструмента, а не весь план. Це і є різниця з
[E1](../e1_human_in_the_loop/).

```
storage_agent → list_buckets (вільно)
              → delete_bucket ─ prod-* ─→ adk_request_confirmation → людина: так/ні → видалити / заблоковано
                              └ інше ───→ видалити без питання
```

## Запуск

```bash
go run .                                    # людина підтверджує видалення prod-logs, без ключа
go run . -input "Delete bucket tmp-cache"    # не-prod: підтвердження не потрібне
go run . -live                              # агент на моделі з apps/.env
go run . console                            # ви — та людина, що підтверджує
go run . web api webui                      # Web UI на http://localhost:8080/ui/
```

Очікуваний слід офлайн-демо:

```
🔧 storage_agent → list_buckets({})
🔧 storage_agent → delete_bucket({"bucket":"prod-logs"})
📦 storage_agent ← delete_bucket = {"error":"error tool \"delete_bucket\" requires confirmation, please approve or reject"}
🙋 storage_agent asks a human: {"originalFunctionCall":{…"name":"delete_bucket"},"toolConfirmation":{…}}
👤 human answers adk_request_confirmation: true
📦 storage_agent ← delete_bucket = {"deleted":"prod-logs"}
🤖 storage_agent: Bucket prod-logs deleted.
```

## Go-примітиви (ADK Go v2.4.0)

| Примітив | Роль у демо |
|---|---|
| `functiontool.Config.RequireConfirmationProvider` | `func(deleteArgs) bool` — питати лише для `prod-*` |
| `functiontool.Config.RequireConfirmation` | статичний варіант «питати завжди» (тут не використано) |
| `toolconfirmation.FunctionCallName` (`adk_request_confirmation`) | подія-запит до людини; run паркується |
| `FunctionResponse{Name: "adk_request_confirmation", Response: {"confirmed": bool}}` | відповідь людини, яка відновлює run |
| `agent.Context.RequestConfirmation(hint, payload)` / `ToolConfirmation()` | ручний варіант усередині інструмента (див. `examples/toolconfirmation` в ADK) |

**Чого в Go немає.** `SecurityPlugin` і `PolicyOutcome.CONFIRM`, які джерело
подає як примітиви, у Go v2.4.0 відсутні. Найближчий аналог централізованого
правила — `llmagent.Config.BeforeToolCallbacks` (або `plugin.Config.BeforeToolCallback`):
одне місце для рішення «питати чи ні», але це callback, а не рушій політик.

## Коли брати

Агент планує й читає вільно, а людини потребує лише незворотна дія: забронювати
квиток, видалити бакет, провести повернення коштів. Гранулярність — саме на дії.

## Коли не брати

Коли ризик у плані, а не в окремій дії. Це гранулярність E1.

## Що подивитися в коді

- `needsConfirmation` — політика: гейт лише на `prod-*`. Загейтуйте все — і люди
  почнуть штампувати, а це гірше за відсутність гейта.
- `deleteBucket` виконує побічний ефект лише після підтвердження: ADK викликає
  обробник удруге, коли надходить `confirmed: true`.
- `TestConfirmationGate` перевіряє всі гілки: підтверджено, відхилено,
  «запарковано без людини», не-prod без питання, невідомий бакет.

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.4.0`
