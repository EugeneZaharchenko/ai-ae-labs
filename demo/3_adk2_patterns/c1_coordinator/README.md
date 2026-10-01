# C1 · Coordinator / Dispatcher

**Хто вирішує наступний крок:** модель · **Категорія:** C · наступний крок обирає модель

Центральна LLM аналізує кожен запит і в рантаймі делегує його спеціалізованому
під-агенту. Під-агенти працюють у `ModeSingleTurn`: кожен виконується у власній
ізольованій гілці сесії й повертає один результат — контекст координатора не
росте від їхніх внутрішніх кроків.

```
coordinator ─(billing_agent{request})→ billing_agent ─┐
            └(tech_agent{request})───→ tech_agent ────┴→ coordinator підсумовує
```

## Запуск

```bash
go run .                                     # сценарне демо, без ключа
go run . -input "The app crashes on login"   # інший під-агент
go run . -input "hello"                      # координатор перепитує
go run . -live                               # та сама конфігурація на моделі з apps/.env
go run . console                             # діалог у консолі
go run . web api webui                       # Web UI на http://localhost:8080/ui/
```

Очікуваний слід офлайн-демо:

```
🔧 coordinator → billing_agent({"request":"I was charged twice for my subscription this month"})
🤖 billing_agent: Refund of the duplicate charge opened as case BIL-7.
📦 coordinator ← billing_agent = {"result":"Refund of the duplicate charge opened as case BIL-7."}
🤖 coordinator: Handled by billing_agent: Refund of the duplicate charge opened as case BIL-7.
```

## Go-примітиви (ADK Go v2.5.0)

| Примітив | Роль у демо |
|---|---|
| `llmagent.Config.SubAgents []agent.Agent` | спеціалісти, яких бачить координатор |
| `llmagent.ModeSingleTurn` | під-агент досяжний як виклик функції з його іменем і аргументом `{"request": …}`; без чату з користувачем |
| `llmagent.ModeTask` | альтернатива: під-агент може перепитувати користувача (тут не використано) |
| `llmagent.ModeChat` | класична передача через `transfer_to_agent` — без ізоляції гілки |

## Коли брати

Запити неможливо перелічити наперед (служба підтримки з відкритим входом), і
кожен запит потребує іншого спеціаліста.

## Коли не брати

Шляхи, критичні до затримки чи вартості, і будь-де, де спрацює
[B4](../b4_conditional_route/): класифікатор плюс детермінований диспетч коштує
один дешевий виклик, координатор — повний виклик моделі на кожен стрибок.
Тут це видно: 2 виклики моделі координатора + 1 виклик спеціаліста на запит.

## Що подивитися в коді

- `coordinatorBrain` — офлайн-заміна рішення «кому делегувати». Виклик має ім'я
  під-агента, а не `transfer_to_agent`: так рантайм подає single-turn під-агентів
  (`internal/workflowinternal/single_turn_tool.go`).
- `specialist` — `Mode: llmagent.ModeSingleTurn`. Результат повертається
  координатору як `{"result": …}`.
- Обмеження Go: Task-режимного агента не можна ставити статичним вузлом графа —
  `validateNoTaskModeGraphNodes` відхилить такий граф. Дозволено лише як
  під-агента координатора або через `workflow.RunNode`.
- Офлайн-мозок бачить усю історію сесії, тож у `console` друге запитання в тій
  самій сесії може отримати відповідь попереднього спеціаліста. У `-live` так не
  буде — там рішення ухвалює модель.

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.5.0`
