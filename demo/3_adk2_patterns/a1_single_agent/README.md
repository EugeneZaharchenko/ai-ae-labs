# A1 · Single Agent

**Хто вирішує наступний крок:** модель · **Категорія:** A · один цикл міркування

Один `llmagent`, один системний промпт, набір інструментів. Модель сама обирає,
які інструменти викликати і в якому порядку.

```
Start → support_agent (LlmAgent + Tools) ⟲ get_order / check_delivery → відповідь
```

## Запуск

```bash
go run .                                   # сценарне демо, без ключа
go run . -input "where is my parcel"       # інший вхід
go run . -live                             # та сама конфігурація на моделі з apps/.env
go run . console                           # діалог у консолі
go run . web api webui                     # Web UI на http://localhost:8080/ui/
```

Очікуваний слід офлайн-демо:

```
🔧 support_agent → get_order({"order_id":"ORD-42"})
📦 support_agent ← get_order = {...,"status":"shipped"}
🔧 support_agent → check_delivery({"order_id":"ORD-42"})
🤖 support_agent: Order ORD-42 is shipped with NovaPoshta, ETA 2026-10-01.
```

## Go-примітиви (ADK Go v2.5.0)

| Примітив | Роль у демо |
|---|---|
| `llmagent.New(llmagent.Config{…})` | сам агент: `Model`, `Instruction`, `Tools` |
| `functiontool.New(cfg, handler)` | типізований інструмент; схема виводиться з Go-структури |
| `Config.Tools []tool.Tool` | Go-аналог Python-ського `tools=[…]` |

## Коли брати

Багатокрокова задача, яку тягне один промпт і кілька інструментів. Більшість
задач, яким «потрібен агент», потребують рівно одного.

## Коли не брати

Понад десяток інструментів або промпт із кількома секціями «якщо користувач
питає про X…». Це сигнал ділити (→ [B4](../b4_conditional_route/),
[C1](../c1_coordinator/)).

## Що подивитися в коді

- `brain` — офлайн-заміна моделі: вона вирішує крок за станом запиту (які
  інструменти вже повернули результат). У режимі `-live` цю роль грає
  `Instruction`, а `brain` не використовується.
- `orderID` — без ідентифікатора агент перепитує, а не вигадує дані.

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.5.0`
