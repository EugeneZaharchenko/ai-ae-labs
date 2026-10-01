# D2 · Iterative Refinement

**Хто вирішує наступний крок:** код · **Категорія:** D · якість, куплена за додаткові виклики

D1 у циклі. Генератор пише, **детермінований** перевіряч на Go ставить оцінку й
повертає прогалини як зворотний зв'язок, і цикл повторюється. Цикл зупиняється
на порозі якості або на ліміті ітерацій. Зберігається **найкращий** варіант, а
не останній.

```
Start → refine (DynamicNode)                              → report
          for i := 1; i <= maxIterations; i++ {
              draft := RunNode(writer, brief + "Fix: …")
              score(draft)  → best? → threshold? break
          }
```

## Запуск

```bash
go run .                     # без ключа: 50 → 75 → 50 (регресія) → найкращий #2
go run . -live               # writer на моделі з apps/.env; перевіряч той самий
go run . console             # діалог у консолі
go run . web api webui       # Web UI на http://localhost:8080/ui/
```

Очікуваний слід офлайн-демо:

```
🤖 writer: A jacket for hikers. Waterproof.
🤖 writer: Waterproof, breathable trail jacket for hikers.
🤖 writer: Waterproof jacket for only €129 — the one jacket every hiker, climber and …
⚙️  report ⇒ attempt 1: score 50; attempt 2: score 75; attempt 3: score 50;
             stop: cap of 3 iterations; kept best, not last → best #2 (75): …
```

## Go-примітиви (ADK Go v2.5.0)

| Примітив | Роль у демо |
|---|---|
| `workflow.NewDynamicNode[IN, OUT](name, fn, cfg)` | цикл — звичайний Go `for` у тілі вузла |
| `workflow.RunNode[string](ctx, child, input)` | один прохід writer-а; кожен виклик отримує власний run id |
| `workflow.NewAgentNode(llmagent, cfg)` | writer як дочірній вузол динамічного вузла |
| `workflow.Chain(Start, refine, report)` | статичний каркас навколо циклу |

Альтернатива без графа — `loopagent.New` з `Config.MaxIterations`. Тут обрано
динамічний вузол, бо «зберегти найкращий» і детермінований перевіряч — це
звичайний Go-код.

## Коли брати

Результат покращується з проходами, і є чим його виміряти: компілятор, набір
тестів, валідатор схеми, як тут `score`. З детермінованим перевірячем це один
із найнадійніших патернів якості.

## Коли не брати

Шляхи, чутливі до часу; розгортання з обмеженим бюджетом; і будь-що, де сигнал
якості суб'єктивний. Якщо сигнал — думка моделі, ви купуєте не якість, а
дисперсію. Тоді достатньо одного проходу — [D1](../d1_review_critique/).

## Що подивитися в коді

- `maxIterations` — жорсткий ліміт. Валідатор графа ловить лише цикл **без**
  маршруту (`ErrUnconditionalCycle`); цикл у Go-коді без лічильника він не
  побачить.
- `score` — детермінований тест виходу. Модель лише пише; оцінює код.
- `refine` зберігає `best`, а не останню спробу. Прохід 3 навмисно гірший за
  прохід 2 — це найдешевша страховка в модулі, один рядок коду.
- `refine` не залежить від ADK, тому `TestRefine` перевіряє ранню зупинку й
  регресію звичайною функцією замість моделі.

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.5.0`
