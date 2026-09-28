# B4 · Conditional Route

**Хто вирішує наступний крок:** код · **Категорія:** B · детермінована оркестрація

Один дешевий класифікатор, далі код відправляє запит рівно в одну з типізованих
гілок. Гілка `Default` ловить усе, що класифікатор не впізнав.

```
Start → classify (LlmAgent) → dispatch ─BUG─────→ bug_desk
                                        ├SUPPORT─→ support_desk
                                        └Default─→ human_triage
```

## Запуск

```bash
go run .                                        # BUG-гілка, без ключа
go run . -input "how do I reset my password"    # SUPPORT-гілка
go run . -input "invoice please"                # Default-гілка
go run . -live                                  # класифікує реальна модель
go run . console
```

Очікуваний слід офлайн-демо:

```
🤖 classify: BUG
⚙️  triage ⇒ BUG
🔀 triage route=[BUG]
⚙️  triage ⇒ bug_desk: ticket filed for engineering
```

## Go-примітиви (ADK Go v2.4.0)

| Примітив | Роль у демо |
|---|---|
| `workflow.NewAgentNode(llmagent, cfg)` | класифікатор як вузол графа |
| `workflow.NewFunctionNode` що повертає `*session.Event` | `dispatch` ставить `ev.Routes = []string{label}` |
| `EdgeBuilder.AddRoutes(from, map[string]Node)` | звичайний `switch` за рядковим маршрутом |
| `EdgeBuilder.AddRoute(from, to, workflow.Default)` | гілка для некласифікованого випадку |
| `workflow.ErrMultipleDefaultRoutes` | два `Default` з одного вузла граф не збере (див. тест) |

## Коли брати

Категорії запиту можна перелічити. Класифікатор плюс детермінований диспетч
коштує один дешевий виклик — координатор ([C1](../c1_coordinator/)) платить
повний виклик моделі на кожен стрибок.

## Коли не брати

Категорії не перелічити або вони сильно перекриваються — тоді C1.

## Що подивитися в коді

- `label` нормалізує відповідь моделі до відомої мітки. Модель може сказати
  «It is a bug.» — маршрут усе одно `BUG`. Невідома мітка не ставить маршруту,
  і спрацьовує лише `Default`.
- Класифікатор нічого не вирішує про потік: він лише називає мітку. Потік
  обирає код (`dispatch` + ребра), тому поведінку видно з графа.
- `TestTwoDefaultsRejected` — запобіжник, який ловить сама валідація графа.

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.4.0`
