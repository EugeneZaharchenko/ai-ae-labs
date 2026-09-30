# C3 · Swarm

**Хто вирішує наступний крок:** модель · **Категорія:** C · наступний крок обирає модель

Кілька рольових агентів сперечаються на спільній «дошці» без координатора, доки
всі не погодяться або доки жорсткий ліміт раундів не зупинить суперечку.

> **У ADK Go v2.4.0 примітива для рою немає.** Тут він зібраний вручну, як і
> описує каталог: динамічний вузол зі звичайним Go-циклом, спільна дошка і
> `workflow.RunNode` для ходу кожної ролі.

```
debate (DynamicNode) ⟲ раунд ≤ maxRounds (4):
    engineer → cost → designer      (кожен читає дошку й дописує в неї)
    усі AGREE? → консенсус
```

## Запуск

```bash
go run .                                   # сценарне демо, без ключа: консенсус у раунді 2
go run . -input "Design a wall mount"      # інша тема
go run . -live                             # та сама конфігурація на моделі з apps/.env
go run . console                           # діалог у консолі
go run . web api webui                     # Web UI на http://localhost:8080/ui/
```

Очікуваний слід офлайн-демо:

```
🤖 engineer: engineer: PROPOSE 5 m cable, 32 A, liquid-free copper
🤖 cost: cost: PROPOSE 7 m is too expensive; cap at 5 m and 16 A
🤖 designer: designer: PROPOSE needs a holster and a coiled 5 m cable
🤖 engineer: engineer: AGREE 5 m coiled cable, 32 A, holster included
🤖 cost: cost: AGREE 5 m coiled cable, 32 A, holster included
🤖 designer: designer: AGREE 5 m coiled cable, 32 A, holster included
⚙️  debate ⇒ consensus in round 2: 5 m coiled cable, 32 A, holster included
```

## Go-примітиви (ADK Go v2.4.0)

| Примітив | Роль у демо |
|---|---|
| `workflow.NewDynamicNode[IN,OUT](name, fn, cfg)` | тіло рою — звичайний Go-цикл раундів |
| `workflow.RunNode[OUT](ctx, child, input)` | хід однієї ролі; працює лише всередині динамічного вузла (`ErrInvalidRunNodeContext`) |
| `workflow.NewAgentNode(agent, cfg)` | роль як вузол; за замовчуванням single-turn — бачить лише свій вхід |
| `maxRounds` (ваш код) | жорсткий кап — фреймворк його не ставить |

## Коли брати

Цінність — у самій суперечці ролей із реально конфліктними цілями (інженер,
вартість, дизайн). Дерево ([C2](../c2_hierarchical_decomposition/)) суперечку
представити не може.

## Коли не брати

Майже завжди. Спершу спробуйте [D1](../d1_review_and_critique/) — він дає більшу
частину виграшу за частку ціни. Рій — найдорожчий і найменш відтворюваний
патерн каталогу: тут навіть офлайн це 6 викликів моделі (у гіршому разі —
`maxRounds × 3 = 12`).

## Що подивитися в коді

- `turnInput` — дошка є **єдиним** спільним контекстом: ролі не читають сесій
  одна одної, лише текст дошки.
- `debate` — умова виходу детермінована (усі написали `AGREE`), а кап —
  незалежний від неї запобіжник. `TestSwarm` перевіряє, що впертий `cost`
  зупиняється рівно на `maxRounds`.
- **Відтворюваність.** Офлайн-мозки сходяться в раунді 2 завжди. На живій моделі
  збіжність не гарантована, а конкретний запуск майже неможливо повторити для
  дебагу — скажіть це замовнику до того, як будувати рій.

---
Станом на 09/2026 · `google.golang.org/adk/v2 v2.4.0`
