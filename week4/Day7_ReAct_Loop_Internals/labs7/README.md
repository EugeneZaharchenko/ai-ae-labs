# ДЗ 7 — ReAct-цикл руками і в графі

Один і той самий цикл Thought → Action → Observation написаний двічі:

- `react.go` — `Run()`, звичайний `for` без ADK;
- `graph.go` — `NewGraphAgent()`: динамічний вузол `react_loop` у циклі викликає через `workflow.RunNode` два дочірні вузли: `plan_step` (думка) і `call_tools` (дія + спостереження).

Обидві версії використовують спільні `checkBudget`, `checkStep` і `dispatch` з `react.go`, тому ліміт, текст помилки й порядок викликів у них однакові.

## Запуск

```bash
go test -race ./week4/Day7_ReAct_Loop_Internals/...       # без ключа й мережі
go run ./week4/Day7_ReAct_Loop_Internals/labs7 graph console
```

Без провайдера мозок — `RulePlanner`. Вивід `graph console` (з реального запуску):

```
модель: офлайн (провайдера не знайдено — …)
User -> Investigate the payout incident for merchant A-114
Agent -> [1] thought: Check the runbooks and the merchant payouts at the same time; the two reads do not depend on each other
    obs [c1] search_internal_wiki: 3 hits: event-1 (rejected), event-2 (merged into incident #418), event-3 (open)
    obs [c2] get_merchant_payouts: 2 payouts pending since 03:10; last settled batch #418 delayed, acquirer says unrelated (since 03:00)
    …
[2] thought: Enough evidence; file a ticket
    obs [c3] create_jira_ticket: LDG-1
[3] thought: Ticket created, reporting back
answer: Related incident #418 found; acquirer says unrelated. Ticket: LDG-1
```

Планувальник, що ніколи не завершує (`endless` у тестах), зупиняється на `maxIterations = 5` в обох версіях:
`вичерпано ліміт 5 ітерацій: iteration limit reached without a final answer`, і `errors.Is(err, ErrMaxIterations)` повертає true
(`TestRunStopsAtIterationLimit`, `TestGraphStopsAtIterationLimit`).

## Руками vs рушій

| Рушій бере на себе | Лишається на інженері |
|---|---|
| Event log (`session.Event`) для кожного кроку | Умови зупинки та `Budget` (ітерації, токени, час) |
| Межі вузлів і типізований вхід/вихід `RunNode[StepResult]` | Кореляція calls ↔ results за `CallID` |
| Налаштовані retry/timeout policy вузла | Дозвіл дії: що агенту взагалі можна викликати |
| Schema-валідація typed tool node | Ідемпотентність side-effecting calls |
| Шлях кожного запуску вузла (`react_loop@1/plan_step@2`) — видно, на якій ітерації впало | Що робити з кроком без `calls` і без `final` (`ErrEmptyStep`, інакше цикл крутиться вхолосту) |
| Лаунчер: console / web без власного коду | Помилка інструмента — це Observation для моделі, а не падіння циклу |
| Сесія і стан між ходами | Історія для планувальника: Observation має повернутися в `History`, інакше наступний крок його не побачить |

У мануальній версії весь цикл видно в одній функції. Трасу (`Trace`) ми збираємо самі й повертаємо навіть разом з помилкою, а тестується все звичайним викликом функції, без runner-а і сесії. У графовій версії той самий цикл дає event log без жодного рядка логування: кожна ітерація — це події `plan_step@N` і `call_tools@N` з типізованим виходом. До кожного вузла можна окремо додати retry/timeout через `NodeConfig`, а агент одразу запускається в console і web. Але рушій не знає, коли цикл треба зупинити. Ліміт ітерацій, токенний бюджет, дедлайн, перевірка порожнього кроку й детекція зациклення — це все наш код у `checkBudget` і `checkStep`, однаковий для обох версій. Кореляцію за `CallID` теж робимо ми: `dispatch` кладе кожне Observation на місце його виклику, а `History.Append` зіставляє результати з викликами за ID, а не за позицією. Руками варто писати, коли потрібен повний контроль над циклом, мінімум залежностей або дуже швидкий цикл (наприклад, всередині іншого сервісу чи майбутнього Temporal workflow). Граф варто брати, коли потрібні аудит кроків, HITL, retry policy вузлів і UI без додаткового коду. Окрема помилка, на яку я натрапив: у шаблоні історія жила в змінній замикання, тобто дві сесії одного агента ділили б її. Тому я передаю `History` у `plan_step` як вхід вузла.

**Fan-out і порядок.** Незалежні read-only calls (`search_internal_wiki`, `get_merchant_payouts`) можна fan-out-ити: `dispatch` запускає їх паралельно, а Observation збирає за індексом виклику, тож порядок результатів не залежить від того, хто закінчив першим. Side-effecting calls (`create_jira_ticket`, позначені `Tool.SideEffecting`) виконуються після читань, по одному, у тому порядку, в якому їх попросив планувальник, бо від їхнього порядку залежить стан зовнішнього світу. Перевіряє це `TestDispatchFanOut`: два читання чекають одне на одного (якщо вони йдуть послідовно, тест падає за таймаутом), а записи йдуть у порядку `write_1, write_2`.

## Що ще зроблено (++ Advanced)

- **Детекція зациклення.** Той самий виклик з тими самими args (без урахування `CallID`) третій раз поспіль → `ErrLoopDetected` ще до `maxIterations`. Третій виклик уже не виконується. Тести `TestRunLoopDetected` і `TestGraphLoopDetected`: бюджет 5, цикл зупиняється на 3-й ітерації після 2 викликів інструмента.
- **Безпечний fan-out** — `dispatch` і `TestDispatchFanOut`, описані вище.

Також є токенний бюджет (`Budget.MaxTokens`, `ErrTokenBudget`) і дедлайн на весь цикл (`Budget.MaxDuration`). Вони перевіряються перед кожним кроком планувальника.
