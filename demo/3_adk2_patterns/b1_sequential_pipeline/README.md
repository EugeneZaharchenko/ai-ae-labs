# B1 · Sequential Pipeline

**Хто вирішує наступний крок:** код · **Категорія:** B · детермінована оркестрація

Фіксований лінійний порядок кроків: вихід кожного — вхід наступного. Порядок не
змінюється ніколи, тож платити моделі за його «перевідкриття» на кожному
запиті — марнотратство. Функційні вузли й агент змішуються в одному ланцюгу.

```
Start → extract (fn) → clean (fn) → summarize (LlmAgent) → load (fn)
```

## Запуск

```bash
go run .                                   # сценарне демо, без ключа
go run . -input "Ticket:   "               # порожнє тіло — ланцюг падає на extract
go run . -live                             # summarize на моделі з apps/.env
go run . console                           # діалог у консолі
go run . web api webui                     # Web UI на http://localhost:8080/ui/
```

Очікуваний слід офлайн-демо:

```
⚙️  extract ⇒ {"sender":"anna@example.com","body":" my card was charged TWICE …"}
⚙️  clean ⇒ {"sender":"[email]","body":"my card was charged TWICE for order ORD-42. …"}
🤖 summarize: SUMMARY: my card was charged TWICE for order ORD-42.
⚙️  load ⇒ loaded → SUMMARY: my card was charged TWICE for order ORD-42.
```

## Go-примітиви (ADK Go v2.4.0)

| Примітив | Роль у демо |
|---|---|
| `workflow.Chain(nodes…)` | будує ребра ланцюга `Start → … → load` |
| `workflow.NewFunctionNode(name, fn, cfg)` | типізовані кроки `extract`, `clean`, `load` |
| `workflow.NewAgentNode(llmagent, cfg)` | `summarize` — єдиний крок, якому потрібна модель |
| `workflowagent.New(Config{Edges})` | граф як агент |

Альтернатива без графа — `sequentialagent.New`; перехід даних між етапами тоді
йде через `llmagent.Config.OutputKey`.

## Коли брати

Витягти → почистити → перевірити → завантажити. Кожен етап отримує маленький
сфокусований промпт, і це підвищує точність етапу.

## Коли не брати

Коли процес мусить пристосовуватися (→ [B4](../b4_conditional_route/)), або коли
етапи незалежні — тоді [B2](../b2_fan_out_gather/) зробить їх одночасно.

## Що подивитися в коді

- `clean` маскує email **до** того, як текст бачить модель: детермінований
  крок перед LLM — дешевий і перевірюваний запобіжник.
- Вузол агента отримує структуру `ticket` як JSON-текст: ADK сам серіалізує
  вхід вузла в повідомлення користувача.
- `extract` повертає помилку на порожньому тілі — ланцюг зупиняється на цьому
  кроці, а не передає сміття далі.

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.4.0`
