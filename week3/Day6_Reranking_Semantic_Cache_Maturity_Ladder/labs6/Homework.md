# Практичне завдання 6 — Agentic GraphRAG: hybrid retrieval, multi-hop planning, re-ranking, semantic cache та eval harness

> **Станом на 09/2026 (перед використанням потрібно перевірити):**
> - ADK Go v2.4.0 (`google.golang.org/adk/v2`) — сигнатури `workflow.NewEmittingFunctionNode`, `workflow.NewJoinNode`, `workflow.NewEdgeBuilder` (`AddFanOut`/`AddFanIn`) відповідають [прикладам `examples/workflow/`](https://github.com/google/adk-go/tree/v2.4.0/examples/workflow).
> - **Go 1.27** — `go`-директива модуля лаб (`go.mod`, звірено 13.09.2026). Не плутати з `go 1.26.6` у `go.mod` самого ADK v2.4.0: то мінімум для залежності, а не версія, яку має мати студент.
> - Reranker `BAAI/bge-reranker-v2-m3` — приклад-кандидат, не вимога.
> - Поріг semantic cache `0.92` — стартова гіпотеза, не константа; калібрується на власному корпусі.

## Легенда

Минулого разу (ДЗ 5) ми зробили пошуковий **індекс**: lossless PDF-парсер (docling-mcp), ієрархічний chunking, сутності з provenance і vector + graph представлення для архіву LEDGERWORKS — 5 000 PDF: звіти PCI DSS, vendor SOC2 підрядників і договори з еквайрами. Але індекс без **розумного retrieval-шару** — це мертвий вантаж. Оксана ставить запит, яким ми закінчили минулий вебінар: *«які мерчанти на тарифі T-2 підпадають під вимогу НБУ 2026 і хто підписував їхні договори?»*, і наївний vector-RAG повертає одну з двох половин відповіді, бо не вміє поєднувати структурний фільтр (тариф + вимога НБУ) із семантичним пошуком і graph-traversal `Merchant → Contract → Signer`.

Цього разу ми додаємо **мозок**: агент-планувальник, який сам вирішує, коли використати vector retriever, коли — graph filter, коли зробити ще один hop; re-ranker як спільний шар оцінки доказів; семантичний кеш для повторюваних запитів юристів; і **eval harness**, який вимірює, чи стало справді краще за vector-RAG baseline. Це — пошукова половина **Макро-артефакту №2** курсу — `Agentic GraphRAG Engine`. Наступний місток — event-sourced operational state і LLM-Wiki / OKF у Вебінарі 9; фінальний capstone успадкує цей engine як knowledge substrate з provenance, budget та eval, а не будуватиме retrieval наново.

## Ранній win: перший видимий результат за ≤15 хвилин

Стартер працює **без API-ключа і без завершеного ДЗ 5**:

1. `go version` — потрібен **Go 1.27+** (`go`-директива в `go.mod`) → `go mod tidy` — без помилок.
2. `go run . console` (з теки `labs6`) і подайте запит *«Що таке тариф T-2?»*. Корпус стартера — `testdata/chunks.json`: той самий документ LEDGERWORKS із ДЗ 5 плюс документ про вимогу НБУ 2026, уже нарізаний у форматі `Chunk` із ДЗ 5.
3. **Checkpoint:** у консолі — відповідь вузла `answer` із provenance: `[cache_hit=false] Топ-результат (c-tariff-T2, ledgerworks_soc2): Тариф T-2 — …`. Повторіть той самий запит — `[cache_hit=true]`. Повний шлях `search → rerank → answer` видно в event log і тестах.

4. `go test ./...` — теж **зелено з коробки**. У стартері є `main_test.go`: фейк-контекст на `agent.NewStrictContextMock`, табличні тести на `search`, наскрізний `TestPipeline_EarlyWin` (перевіряє `chunk_id` і `document_id` у відповіді, тобто provenance), `TestCache_ExactHit`, `TestCache_ConcurrentAccess` (під `-race`) і `TestBaseline_ConfusesTariffs` — він показує, що baseline-пошук ставить чанк про T-1 серед кандидатів на запит про T-2.

**Checkpoint:** `ok … 8 passed, 2 skipped` (`go test -v .`). Два пропущені — це **критерії оцінювання**, а не заготовки: `TestRerank_ChangesOrder` (15 балів) і `TestCache_ParaphraseHits` (10 балів). Знімайте `t.Skip` тоді, коли берете відповідний пункт, і дозвольте тесту падати — це ваша специфікація. Пара «до/після» з першого з них іде просто в README як один із п'яти обов'язкових прикладів.

`search` у стартері — **baseline** (частка слів запиту в чанку), не векторний пошук: його замінюєте ви (пункт 3, ++ Advanced). Свій індекс із ДЗ 5 підключайте так: `CHUNKS=шлях/до/chunks.json go run . console`.

## Основне завдання

**Ваше завдання —** завершити `Agentic GraphRAG Engine` (Макро-артефакт №2) на корпусі з ДЗ 5, стартуючи з [week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/main.go](https://github.com/dimetron/ai-ae-labs/blob/main/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/main.go):

1. **Агент-планувальник як workflow-граф.** Зберіть конвеєр `Start → classify → (decompose) → [vector_search | graph_filter | graph_traversal] → JoinNode → rerank → format` через `workflow.NewEdgeBuilder` із `AddFanOut` / `AddFanIn`. Використайте `workflow.NewEmittingFunctionNode` для класифікатора (`single-hop` / `multi-hop` / `global` / `metadata-filter`) і `workflow.NewJoinNode` для збору результатів із кількох гілок (еталонний патерн — [`examples/workflow/complex`](https://github.com/google/adk-go/blob/v2.4.0/examples/workflow/complex/main.go); топологію змінюєте в `graph.go`). Обов'язковий **budget retrieval-кроків (3–5)** захищає від нескінченного планування: terminal `FunctionNode` повідомляє «недостатньо доказів», якщо немає набору доказів із provenance за локально відкаліброваним критерієм.

2. **Multi-hop запити з provenance.** Запит Оксани (`тариф T-2, НБУ 2026, хто підписав`) має повернути **обидва** речення з минулого тижня (список мерчантів на T-2 + підписант по кожному) із посиланнями на конкретні `chunk_id` / `document_id` / `span`. Підзапити йдуть до різних retriever-ів (`vector` для семантики, `graph_filter` для тарифу й вимоги НБУ, `graph_traversal` для зв'язку Contract → Signer) і об'єднуються через `JoinNode` у `map[nodeName]RetrievalResult` перед `rerank`.

3. **Re-ranking як спільний шар.** Інтегруйте мультимовний cross-encoder reranker (наприклад, `BAAI/bge-reranker-v2-m3`; перевірте model card, ліцензію та input schema) у конвеєр: перший етап — vector/graph retriever повертає top-K (наприклад, K=20); другий — reranker переранжує до top-N (N=3). Задокументуйте **5 запитів, де reranking змінив топ-результат** (таблиця «до/після» з groundedness та top-1 relevance) і як ви відкалібрували quality gate на своєму gold set.

4. **Семантичний кеш.** Додайте вузол `cache_check` на початку графа: cosine-similarity між embedding запиту й embedding кешованих запитів. Поріг оберіть і обґрунтуйте на своєму corpus/gold set; `0.92` може бути стартовою гіпотезою, але не є вимогою. Ключ має ізолювати tenant, permission/policy boundary, версію корпусу та TTL. **Hit** → відповідь із provenance; **miss** → повний конвеєр. Продемонструйте в README: один запит → miss; перефразований (*«скільки коштує X?»* ≈ *«яка ціна X?»*) → hit; виміряйте exact-hit, semantic-hit і miss. Поясніть, чи кешуєте фінальну відповідь або проміжні `RetrievalResult`, і як інвалідуєте записи після оновлення корпусу.

5. **Retrieval eval harness.** Зберіть набір із **20 запитів** (10 single-hop, 10 multi-hop) і проженіть кожен через:
   - **Vector-RAG baseline** — один embeddings-пошук top-5 → LLM-відповідь.
   - **Agentic GraphRAG** — ваш сьогоднішній агент.

   Для кожного запиту заміряйте: `top1_relevance` (1-5, людська оцінка), `groundedness` (чи відповідь підтверджується чанками), `latency_p95`, `tokens_used`. Підсумкова таблиця — у README, з **обґрунтуванням**, на яких типах запитів Agentic виграє, а де vector-RAG достатній.

6. **Decision sheet.** У README заповніть односторінковий decision sheet «vector vs GraphRAG vs agentic» для вашого конкретного корпусу: на якому щаблі **Retrieval Maturity Ladder** стоїть задача, чому, і який наступний щабель розглядатимете, якщо query patterns зміняться.

## Альтернативні теми (на вибір)

Та сама механіка (planner → fan-out vector/graph → JoinNode → rerank → cache → eval), інший корпус:

- **Security:** пошук по базі CVE-описів вашого стеку — «які вразливості стосуються нашої версії Postgres?» з re-ranking за релевантністю версії та multi-hop через зв'язок CVE → affected_versions → fixed_in. Окремий інтерес: graph traversal від CVE до vendor advisories.
- **Research:** пошук по конспектах статей із ДЗ 5 — багатослівні дослідницькі питання, де перший кандидат рідко найкращий, і де multi-hop через citations/cocitations працює краще за чистий vector.
- **Fun:** пошук по книзі рецептів — *«щось швидке без духовки з куркою»* (запит, який ламає naive keyword search, але з графом «інгредієнт → страва → час приготування» — працює). Тут особливо добре видно роль multi-hop.

## ++ Advanced (до 20 балів, для сеньйорів)

- **Паралельний гібрид через fan-out:** keyword-гілка (BM25) + vector-гілка через `AddFanOut`, злиття результатів через `JoinNode` і RRF (Reciprocal Rank Fusion) перед re-rank. Еталонний патерн fan-out/JoinNode — [`examples/workflow/complex/main.go`](https://github.com/google/adk-go/blob/v2.4.0/examples/workflow/complex/main.go) (дослідники → gather → format).
- **Eval harness із grounding-автоматикою:** замість ручної оцінки groundedness — LLM-суддя, який перевіряє, чи кожне твердження відповіді має span-посилання в чанках. Запишіть precision/recall grounding-у для 20 запитів.

## 🔥 Бонус-трек (не оцінюється, не потрібен для сертифіката)

**Adaptive planner із budget/latency SLO.** Реалізуйте `AdaptiveConfig{MaxTokens, MaxLatency, MinScore}`: зупиняйтеся, коли локально відкалібрований quality gate підтверджує достатні докази з provenance, або коли `token_counter > MaxTokens`. Запустіть eval harness і порівняйте сумарну вартість та groundedness із/без адаптивного планувальника. Балів не дає; робиться для себе.

## Якщо щось не працює

- **Multi-hop запит не запускає обидві гілки.** Перевірте `EdgeBuilder` у `graph.go`: маршрут `multi-hop` має вести через `AddRoute(classify, decompose, workflow.StringRoute("multi-hop"))` до `AddFanOut(decompose, vectorSearch, graphFilter, graphTraversal)` + `AddFanIn(join, …)`. В event log для одного `invocation_id` мають бути події і `vector_search`, і `graph_filter` / `graph_traversal`. Якщо є лише vector — класифікатор емітить `single-hop` (перевірте, що розпізнаються і тариф, і вимога НБУ).
- **Re-ranker не змінює top-3.** Переконайтеся, що в пул перед re-rank зливаються кандидати з **усіх** гілок, а не лише з vector. Якщо пул правильний, а порядок не міняється — перевірте модель (мультимовність, input schema) і калібрування порога на власному gold set.
- **Кеш завжди miss (або навпаки — хибні hit).** Поріг `0.92` — стартова гіпотеза: занизький дає false-positive на різних за змістом запитах, зависокий не ловить перефразування. Виміряйте false-positive rate на 5–10 парах перефразувань зі свого корпусу і скоригуйте.
- **Немає API-ключа для LLM-декомпозитора.** Це не блокер: у ДЗ 6 декомпозицію дозволено реалізувати rule-based парсером (regex на «тариф T-2, НБУ 2026, хто підписав») — LLM-вузол лишіть як production-опцію.
- **Немає завершеного ДЗ 5 (індексу).** Не блокер: `testdata/chunks.json` — готовий корпус у форматі ДЗ 5 (тарифи, мерчанти, договори, підписанти, вимога НБУ 2026), на ньому працюють і single-hop, і multi-hop запит Оксани. Граф `Merchant → Contract → Signer` для `graph_traversal` побудуйте з цих чанків самі.
- **`corpus: open testdata/chunks.json: no such file`.** Запускайте з теки `labs6` або вкажіть шлях: `CHUNKS=… go run ./week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6 console`.

## Відомі прогалини курсу (follow-up)

- **LLM-Wiki / OKF** як третя нога KB-модуля (поряд із taxonomy/ontology і hybrid retrieval) — закрита у Вебінарі 9 (context engineering). На цьому тижні ми не зобов'язані її реалізовувати, але decision sheet має згадати, що вона існує і куди дивитися.
- **FalkorDB GraphRAG-SDK** — репозиторій є, але в нашій вікі позначений як `gated pending raw`. У ДЗ не вимагається підключати FalkorDB як залежність. Якщо вирішите використати — окремо зафіксуйте це в README як «експериментальне».

## Критерії оцінювання

| Критерій | Бали |
|---|---|
| Робочий agentic retrieval-граф: planner → fan-out (vector + graph) → JoinNode → rerank → format, з обов'язковим budget limit | 25 |
| Multi-hop запит Оксани повертає обидва речення з provenance-посиланнями | 10 |
| Re-ranking з документованим ефектом (5 запитів «до/після» + groundedness) | 15 |
| Семантичний кеш із продемонстрованим hit на перефразованому запиті, обґрунтуванням «що кешуємо» | 10 |
| Eval harness на 20 запитах із таблицею vector-RAG vs Agentic за 4 метриками | 15 |
| Decision sheet: щабель Retrieval Maturity Ladder з обґрунтуванням + README | 5 |
| ++ Advanced (бонус) | 20 |
| **Разом** | **100** |

Базове завдання дає до 80 балів; ++ Advanced — це **+20 додаткових балів** (разом 100). 🔥 Бонус-трек не оцінюється взагалі. Альтернативна тема оцінюється за тими самими критеріями. **Базового завдання достатньо для сертифіката; «🔥 Бонус» ніколи не є його умовою.** Бали за всі 12 завдань підсумовуються: від 60 % відкривається генерація сертифіката, від 85 % — з відзнакою.

## Формат здачі

GitHub-репозиторій з кодом, eval-таблицями та README; `go build ./...` має проходити; `go test ./...` — зелений (тести є у стартері з коробки, тож «якщо є» більше не застосовується: два `t.Skip` мають бути зняті й зелені, бо це критерії на 25 балів разом). README має містити: (а) decision sheet з обґрунтуванням щабля, (б) таблицю 5 запитів «до/після» rerank, (в) демонстрацію cache hit, (г) eval-таблицю 20 запитів із чотирма метриками. Посилання на репозиторій — у форму здачі. Якщо репозиторій закритий — додайте акаунт ментора в collaborators (акаунт указано в інструкції до курсу на платформі).

Стартовий шаблон: [week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/main.go](https://github.com/dimetron/ai-ae-labs/blob/main/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/main.go) · Еталонні приклади: [`examples/workflow/basic`](https://github.com/google/adk-go/blob/v2.4.0/examples/workflow/basic/main.go), [`examples/workflow/routing/string`](https://github.com/google/adk-go/blob/v2.4.0/examples/workflow/routing/string/main.go) (для класифікатора), [`examples/workflow/complex`](https://github.com/google/adk-go/blob/v2.4.0/examples/workflow/complex/main.go) (для fan-out + JoinNode) · Wiki-опори: [hybrid retrieval](https://localaimaster.com/blog/reranking-cross-encoders-guide), [CacheRAG / semantic caching](https://futureagi.com/blog/what-is-semantic-caching-llms-2026). FalkorDB GraphRAG-SDK — case study для слайда, **не залежність** для коду.

## Відео, якщо застрягли (не обов'язкове, посилання звірені 27.08.2026)

Повний список із поясненнями — у матеріалах лекції на платформі курсу, розділ «Відео-опори». Чотири найкорисніші саме під це ДЗ:

- **Пункт 6, decision sheet — не знаєте, як обґрунтувати щабель** — https://www.youtube.com/watch?v=w9u11ioHGA0. Автор розкладає ті самі техніки за осями складність × вплив і дає рамку «baseline → loss analysis → вибір за complexity-adjusted impact». Це буквально форма вашого decision sheet.
- **++ Advanced, RRF — не розумієте, чому не можна просто скласти скори** — https://www.youtube.com/watch?v=4Xe_iMYxBQc. Звідти видно, чому BM25 узагалі не має верхньої межі (TF, IDF, field-length norm), тобто чому сума з косинусом мовчки віддає перемогу одному retriever-у.
- **Пункт 5, eval — чотирьох метрик мало** — https://www.youtube.com/watch?v=wRJD0inpmjU. Три reference-free метрики, які варто додати: answer completeness, document relevance, hallucination detection. І виміряний trade-off: вища повнота відповіді збільшує поверхню для галюцинацій.
- **Пункт 4, кеш — плутаєте його з кешем провайдера** — https://www.youtube.com/watch?v=SkM4k4SKvCM. Це запис про **інший** кеш (prompt/KV: вхідні токени, хвилини життя, вмирає від таймстемпа в system prompt). Подивіться саме щоб побачити, чим він не є вашим семантичним кешем.

**Українською**, якщо хочеться спершу почути поняття рідною мовою: https://www.youtube.com/watch?v=e24YTos57y8 (fwdays, 13.08.2026) — naive RAG → hybrid → corrective → GraphRAG.

> Цифри, названі спікерами (приріст якості, hit-rate, латентність), — заявлені ними, не відтворені нами. У README вони не замінюють ваш власний eval: сенс пункту 5 саме в тому, щоб виміряти на **своєму** корпусі.

## Дедлайн

Два тижні після дати відкриття завдання.

| День лекції | Дедлайн |
|---|---|
| Чт 08.10.2026 | Чт 22.10.2026 |

Шановні слухачі!

Дедлайн надсилання розв'язку на перевірку — 23 год. 59 хв. 22.10.2026 (це крайній термін,
рекомендуємо виконати і надіслати розв'язок протягом тижня після відкриття завдання).

**Інструкція до виконання завдання:**

До 23 год. 59 хв. 22.10.2026 року додайте отримані результати виконання завдання в наступному форматі.

Посилання на репозиторій з виконаним завданням з доданим акаунтом автора до collaborators
https://github.com/dimetron — активне посилання.

Натисніть кнопку «Надіслати відповідь та перейти до наступного етапу». Переконайтесь, що з'явився
напис «Чекаємо оцінки викладача».

Очікуйте на оцінку — згодом вона з'явиться під відповіддю у розділі «Оцінка викладача».

**Зверніть увагу!** Надіслати завдання вдруге неможливо — у вас є лише одна спроба.

Слухачам, які служать у ЗСУ, дедлайн продовжуємо за окремим запитом.

