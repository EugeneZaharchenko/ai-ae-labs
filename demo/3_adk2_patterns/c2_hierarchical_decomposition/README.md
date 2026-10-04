# C2 · Hierarchical Decomposition

**Хто вирішує наступний крок:** модель · **Категорія:** C · наступний крок обирає модель

Агенти утворюють багаторівневе дерево: кожен рівень розкладає свою мету на
під-цілі, делегує вниз, а потім синтезує результати вгору. `agenttool.New`
перетворює агента на інструмент, тож батько зберігає контроль над синтезом.

```
lead ─→ research_manager ─→ market_analyst
     │                   └→ competitor_analyst      ↑ синтез угору
     └→ writing_manager  ─→ editor
```

## Запуск

```bash
go run .                                   # сценарне демо, без ключа
go run . -input "Analyse heat pumps"       # інша мета
go run . -live                             # та сама конфігурація на моделі з apps/.env
go run . console                           # діалог у консолі
go run . web api webui                     # Web UI на http://localhost:8080/ui/
```

Очікуваний слід офлайн-демо:

```
🔧 lead → research_manager({"request":"Write a short competitive analysis of the EV charger market"})
📦 lead ← research_manager = {"result":"market: 2.1M public chargers in EU, +28%/yr; competitors: Ionity, Tesla, Allego"}
🔧 lead → writing_manager({"request":"market: 2.1M public chargers in EU, +28%/yr; competitors: …"})
📦 lead ← writing_manager = {"result":"Report: market: …; competitors: Ionity, Tesla, Allego"}
🤖 lead: Report: market: 2.1M public chargers in EU, +28%/yr; competitors: Ionity, Tesla, Allego
```

Аналітиків і редактора в сліді немає — і це головна властивість патерну: кожен
`agenttool` запускає дочірнього агента в **окремій** сесії, тож корінь бачить лише
підсумок рівня нижче, а не все, що бачили нащадки.

## Go-примітиви (ADK Go v2.5.0)

| Примітив | Роль у демо |
|---|---|
| `agenttool.New(agent, cfg)` | агент як інструмент; аргумент `{"request": string}`, результат `{"result": text}` |
| `llmagent.Config.Tools` | діти кожного рівня |
| `workflow.NewWorkflowNode(name, edges)` | альтернатива: вкладений граф як вузол (імена підграфів мусять відрізнятися від батька) |

## Коли брати

Потрібне багаторівневе планування, яке не покриває жоден фіксований конвеєр:
«напиши конкурентний аналіз ринку зарядок для електромобілів».

## Коли не брати

Прості, добре визначені задачі; системи, критичні до затримки чи вартості. Якщо
ви можете назвати кроки — вам потрібен [B1](../b1_sequential_pipeline/). Тут на
один запит припадає 3 виклики моделі `lead`, по 3 у менеджера досліджень і 2 у
менеджера тексту, плюс по одному в кожного листка — 11 викликів.

## Що подивитися в коді

- `node` — один рівень дерева: `llmagent` із дітьми-інструментами. Той самий
  конструктор будує і менеджерів, і листки.
- `delegate` — офлайн-мозок менеджера: викликати кожну дитину один раз, потім
  злити результати.
- `leadBrain` — передає `writing_manager` **результат** досліджень, а не
  сирий запит користувача: синтез іде вгору, потім знову вниз.
- `agenttool` копіює стан батьківської сесії в нову дочірню сесію (крім ключів
  `_adk*`), але подій батька дитина не бачить.

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.5.0`
