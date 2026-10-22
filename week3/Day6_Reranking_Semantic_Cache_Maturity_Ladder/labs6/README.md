# Лабораторна 6 — retrieval: пошук → re-rank → відповідь із provenance + кеш

**Станом на 09/2026:** Go 1.27.1, ADK Go v2.5.0. Жодного LLM, ключа або `.env` для стартера.
Завдання — у [Homework.md](Homework.md).

## Запуск

```bash
cd week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6
go run . console          # запит: Що таке тариф T-2?  (повторіть — буде cache hit)
go test -v -race .        # 8 passed, 2 skipped — контракти rerank і семантичного кешу
```

Свій індекс із ДЗ 5 замість стартового корпусу:

```bash
CHUNKS=../../Day5_Lossless_PDF_Parsing_Chunking/labs5/testdata/chunks.json go run . console
```

Граф стартера (`graph.go`) — лінійний; agentic-граф із fan-out і `JoinNode` — ваша робота (Homework п.1):

```text
Start → search → rerank → answer
```

| Файл | Що в ньому | Ваша робота |
|---|---|---|
| `retrieval.go` | `retriever`: `search` (baseline keyword overlap), `rerank` (заглушка), `answer`, кеш | векторний/BM25 пошук, re-ranker, семантичний кеш |
| `graph.go` | топологія | classify → fan-out → JoinNode → rerank → format, budget |
| `main.go` | лише wiring: корпус + launcher | — |
| `main_test.go` | табличні тести на `StrictContextMock` | зняти два `t.Skip`, переписати тести-заглушки |
| `internal/corpus` | читання `chunks.json` із перевіркою provenance | читати, не змінювати |
| `internal/systemone` | re-rank decision-моделлю `tev1:4b` через `/v1/systemone` (вмикається `SYSTEMONE_URL`) | викликати з `rerank`, не змінювати |
| `internal/embed` | ембединги (чистий Go за замовчуванням, Ollama — через `OLLAMA_EMBED_MODEL`), `Cosine`, `Rerank` | викликати з `rerank` і кешу, не змінювати |
| `testdata/chunks.json` | корпус у форматі `Chunk` із ДЗ 5 | додати свої документи |

## Корпус

`testdata/chunks.json` — 21 чанк із двох документів: `ledgerworks_soc2` (той самий, що в ДЗ 5:
вендори, знахідки, тарифи, договори, підписанти) і `nbu_2026_requirements`. Цього достатньо для
запиту Оксани: *«які мерчанти на тарифі T-2 підпадають під вимогу НБУ 2026 і хто підписував їхні
договори?»* — відповідь потребує трьох hop-ів: тариф → мерчант → договір → підписант.

## Чому baseline навмисно слабкий

`search` рахує частку слів запиту в чанку. На *«яка ставка комісії на тарифі T-2?»* чанк про T-1
(`c-A331-rate`) потрапляє в топ-5 поруч із чанками про T-2 — `TestBaseline_ConfusesTariffs`
показує це. Це вихідна точка для таблиці «до/після» re-ranker-а в README.

## Ембединги

За замовчуванням `internal/embed` працює на чистому Go: детермінований стаб (хешовані триграми символів),
без мережі й ключа — на ньому йдуть `go test` і `go run`. Справжня модель — [Ollama](https://docs.ollama.com/capabilities/embeddings),
вмикається явно:

```bash
ollama pull embeddinggemma
OLLAMA_EMBED_MODEL=embeddinggemma go run . console   # OLLAMA_BASE_URL — якщо не localhost:11434
task week3:day6:web:ollama                           # Web UI з Ollama: перевіряє сервер і тягне модель
```

Рядок `embeddings: …` на старті показує, що саме працює. **Пороги не калібруйте на стабі:** він дає
0.47 для перефразування *«Що таке тариф T-2?» ≈ «Розкажи про тарифний план T-2»* і 0.88 для
*«…T-2?» vs «…T-1?»*, а `embed.Rerank` на стабі ставить чанк про мерчанта на T-1 першим на запит про T-2.
Стаб схожий на рядки, а не на зміст.

Але й модель сама не рятує. `embeddinggemma` (Ollama 0.40, 10/2026) на тих самих парах:

| Пара | стаб | embeddinggemma |
|---|---:|---:|
| «Що таке тариф T-2?» ≈ «Розкажи про тарифний план T-2» (має влучити) | 0.47 | 0.79 |
| «Що таке тариф T-2?» vs «Що таке тариф T-1?» (не має влучити) | 0.88 | 0.79 |
| «Скільки коштує тариф T-2?» ≈ «Яка ціна тарифу T-2?» | 0.42 | 0.95 |

Re-rank на моделі вже правильний (чанк про T-2 — перший), а от для кешу перефразування й інший тариф
мають однаковий cosine: жоден поріг їх не розділить. Потрібен детермінований доказ поверх схожості —
наприклад, однаковий набір кодів (`T-2`, `CT-2025-031`) у запиті й у кешованому запиті.

## Re-rank decision-моделлю (tev1:4b)

[Decision-модель](https://docs.ollama.com/capabilities/decision) — не чат-модель: відповідає на типізовані
питання за один прохід, без генерації тексту. `internal/systemone` питає для кожного кандидата «чи містить
уривок відповідь на запит?» і бере P(yes) як оцінку — це cross-encoder, на відміну від `embed.Rerank`.
Модель за замовчуванням — [`tev1:4b`](https://ollama.com/library/tev1) (Together AI).

```bash
task week3:day6:web:ollama        # тягне embeddinggemma і tev1:4b, вмикає обидва
```

Один запит руками — так виглядає re-rank одного кандидата:

```bash
curl -s http://localhost:11434/v1/systemone -H 'Content-Type: application/json' -d '{
  "model": "tev1:4b",
  "state": {"query": "Яка ставка комісії на тарифі T-2?",
            "passage": "Тариф T-2 — тарифний план зі ставкою комісії 2.9 % для мерчантів, які підпадають під вимогу НБУ 2026."},
  "questions": {"relevant": {"type": "noul", "instructions": "Does the passage contain the answer to the query?"}}
}'
# {"model":"tev1:4b","answers":{"relevant":{"type":"noul","noul":0.915}},"usage":{"input_tokens":167,"output_tokens":0}}
```

`tev1:4b` на корпусі стартера (Ollama 0.40.1, 10/2026):

| Перевірка | tev1:4b | embeddinggemma (cosine) |
|---|---:|---:|
| re-rank «ставка T-2»: чанк про T-2 | **0.92** | 0.73 |
| re-rank «ставка T-2»: чанки про T-1 | 0.004, 0.009 | 0.66, 0.57 |
| кеш: «Що таке тариф T-2?» ≈ «Розкажи про тарифний план T-2» | **0.89** | 0.79 |
| кеш: «Що таке тариф T-2?» vs «Що таке тариф T-1?» | **0.085** | 0.79 |

Для кешу питання інше: state `{"question_a", "question_b"}`, noul *«Do both questions ask for the same
information, so one answer serves both?»* — там, де cosine дає однакові 0.79, decision-модель розділяє.
Ціна — один виклик моделі на кандидата (або на пару запитів), тож рахуйте latency в eval harness.

Чи модель справді працює з українською, перевіряє live eval на Go — `internal/systemone/eval_test.go`:
rerank із hard negatives (інший тариф, договір, мерчант), порядок кандидатів, out-of-domain, кеш і
детермінізм. Без `SYSTEMONE_URL` тести пропускаються.

```bash
task week3:day6:eval:systemone                      # tev1:4b
task week3:day6:eval:systemone RERANK_MODEL=nimble  # те саме для іншої моделі
```

`tev1:4b`, 08.10.2026: rerank TP=10 FP=0 TN=11 FN=0, кеш TP=5 FP=0 TN=5 FN=0, out-of-domain ≤ 0.014.
Одна пастка: модель читає state як JSON-текст. Кирилиця має йти UTF-8, а не `\uXXXX` — Go так і робить,
а Python `json.dumps` без `ensure_ascii=False` ні (той самий запит: 0.867 замість 0.963).

Інші decision-моделі в Ollama: `tev1:0.8b`, `nimble`, `laya`, `clef-flash`, `clef`
([список](https://ollama.com/search?c=decision)); підставляйте через `SYSTEMONE_MODEL`.
LiquidAI d1-3B в Ollama 0.40.1 ще не працює (`unsupported decision encoding "lfm2-d1"`).

## Кеш

Кеш — поле `retriever`, під `sync.Mutex`: коли ви зробите fan-out, вузли працюватимуть паралельно,
і `TestCache_ConcurrentAccess` під `-race` це ловить. Ключ зараз — нормалізований запит (точний
hit). Семантичний кеш, ізоляція tenant/версії корпусу й TTL — Homework п.4.

---

# ДЗ 6 — що зроблено

Agentic GraphRAG над корпусом стартера (`testdata/chunks.json`, той самий LEDGERWORKS із ДЗ 5 + вимога НБУ 2026).
Без LLM і без ключа: планувальник і декомпозиція rule-based (дозволено умовою), ембединги — стаб на чистому Go
або `embeddinggemma` через Ollama.

```bash
go test -race ./...                                   # усе зелене, обидва t.Skip знято
go run . console                                      # стаб-ембединги
OLLAMA_EMBED_MODEL=embeddinggemma go run . console    # справжня модель (поріг кешу 0.80)
EVAL_OUT=eval.md go test -run TestEval .              # таблиці eval у файл
OLLAMA_EMBED_MODEL=embeddinggemma EVAL_OLLAMA=1 EVAL_OUT=eval.md go test -run TestEval -timeout 20m .
```

| Файл | Що в ньому |
|---|---|
| `graph.go` | топологія агента (нижче) |
| `plan.go` | `classify` (EmittingFunctionNode, 4 маршрути), `decompose`, budget |
| `branches.go` | гілки: `vector_search`, `keyword_search` (BM25), `graph_filter`, `graph_traversal` |
| `kg.go` | граф Merchant → Contract → Signer, Tariff → НБУ 2026, кожне ребро зі span-ом |
| `rerank.go` | RRF-злиття гілок, re-ranker, quality gate, маршрут grounded / insufficient |
| `answer.go` | `cache_check`, `format` (відповідь із цитатами), `insufficient` |
| `cache.go` | семантичний кеш: scope (tenant / policy / версія корпусу), TTL, коди + intent + cosine |
| `eval.go`, `eval_test.go` | gold set 20 запитів, vector-RAG vs agentic, таблиця «до/після» rerank |

## 1. Граф

```text
Start → cache_check ─hit──→ cached
                    └miss─→ classify ─multi-hop─→ decompose ─┐
                                     └─single-hop / global / metadata-filter
         ┌───────────────────────────────────────────────────┘
         ├─ vector_search ──┐
         ├─ keyword_search ─┤
         ├─ graph_filter ───├─ join (JoinNode) → rerank ─grounded─────→ format
         └─ graph_traversal ┘                           └insufficient─→ insufficient
```

- `classify` — `workflow.NewEmittingFunctionNode`, емітить подію з `Routes = [клас]`. `multi-hop` іде через
  `AddRoute(classify, decompose, StringRoute("multi-hop"))` → `AddFanOut(decompose, …)`; інші класи йдуть у ті самі
  гілки напряму через `MultiRoute`. `AddFanIn(join, …)` збирає `map[nodeName]RetrievalResult`.
- **Чому всі 4 гілки запускаються завжди.** JoinNode чекає всіх попередників, умовний вхід у нього — помилка
  конфігурації. Тому план каже гілці, чи вона потрібна; непотрібна повертає `skipped` миттєво.
- ADK не дозволяє вузлу (не JoinNode) мати два безумовні входи, тому окремого вузла `direct` немає — тільки маршрут.
- **Budget = 5 кроків.** Крок — це один підзапит до retriever-а (vector + BM25 мають один спільний підзапит) або
  один hop обходу графа. Запиту Оксани потрібно рівно 5: текст, фільтр, 3 hop-и (тариф → мерчант → договір →
  підписант). Якщо budget закінчився, обхід зупиняється з `truncated`, а `rerank` веде у термінальний вузол
  `insufficient`: «Недостатньо доказів: вичерпано budget retrieval-кроків» (`TestGraph_BudgetExhausted`).
- **Критерій доказів (локально відкалібрований):** multi-hop — для кожного мерчанта є повний ланцюжок до
  підписанта зі span-ами; metadata-filter — є хоч один факт графа; single-hop — score топ-1 після rerank ≥ 0.55.

## 2. Запит Оксани (multi-hop)

```text
> Які мерчанти на тарифі T-2 підпадають під вимогу НБУ 2026 і хто підписував їхні договори?
[cache_hit=false] 1) Мерчанти на тарифі T-2, які підпадають під вимогу НБУ 2026: A-114, A-207.
   - T-2 підпадає під вимогу НБУ 2026 [c-nbu-req, nbu_2026_requirements, span 0:95] «Вимога НБУ 2026 поширюється на мерчантів із тарифом T-2»
   - A-114 на тарифі T-2 [c-A114-rate, ledgerworks_soc2, span 0:136]
   - A-207 на тарифі T-2 [c-A207-rate, ledgerworks_soc2, span 0:136]
2) Хто підписував договори:
   - A-114 → договір CT-2025-014 [c-A114-rate, ledgerworks_soc2, span 0:136] → Олег Мельник, CFO, 12.03.2025 [c-sign-014, ledgerworks_soc2, span 0:84]
   - A-207 → договір CT-2025-031 [c-A207-rate, ledgerworks_soc2, span 0:136] → Ірина Бойко, Head of Partnerships, 02.06.2025 [c-sign-031, ledgerworks_soc2, span 0:101]
[plan=multi-hop steps=5/5]
```

Обидва речення є, кожен факт має `chunk_id / document_id / span` (байтові зсуви в тексті чанка). A-331 (тариф T-1)
у відповідь не потрапляє. Декомпозиція: структурна частина (T-2 + НБУ) → `graph_filter`, «хто підписував» →
`graph_traversal`, текстова частина без «і хто…» → vector + BM25. В event log одного invocation є події від
`vector_search`, `graph_filter` і `graph_traversal` (`TestGraph_OksanaMultiHop`).

Граф будую сам із чанків регулярками (`kg.go`): «Мерчант A-… обслуговується за тарифом T-…, … договір CT-…»,
«Договір CT-… підписав(ла) …», «Вимога НБУ 2026 поширюється на мерчантів із тарифом T-…». Для корпусу на 5 000 PDF
це треба замінити на сутності з ДЗ 5 (`entities.go`, provenance вже є).

## 3. Re-ranking

Двоетапна схема: гілки віддають top-K = 20, RRF зливає їх у пул, re-ranker лишає top-N = 3.

- **Cross-encoder:** `tev1:4b` через `internal/systemone` (вмикається `SYSTEMONE_URL`) — читає пару «запит ↔ уривок»
  разом і дає P(yes). Модель у мене не завантажилась (моя версія Ollama її ще не підтримує), тому цифри нижче — без неї.
- **Офлайн re-ranker (за замовчуванням):** `0.5 · cosine + ознаки пари`: +0.5 за кожен код запиту (T-2, CT-2025-031,
  A-114, F-104), який є в чанку; −0.5, якщо в чанку той самий тип коду з іншим значенням (T-1 на запит про T-2);
  +0.6 · частка змістовних слів запиту (перші 5 літер — грубий стемер для українських закінчень); бонус за тип
  питання (підписант / ставка / визначення). Це не нейромережа, але працює так само «попарно», як cross-encoder,
  і пояснює, чому чанк піднявся.

**5 запитів, де rerank змінив топ-1** (`TestRerank_FiveQueries`, стаб-ембединги; grounded = топ-1 містить відповідь):

| # | Запит | Перший етап | top-1 до | top-1 після rerank | score після | grounded до / після |
|---|---|---|---|---|---:|---|
| 1 | Хто підписав договір CT-2025-031? | baseline (keyword) | c-A207-rate | c-sign-031 | 1.74 | ні / так |
| 2 | Хто підписав договір CT-2025-031? | RRF (vector + BM25) | c-sign-014 | c-sign-031 | 1.74 | ні / так |
| 3 | Що сталося зі знахідкою F-104? | vector | c-F101 | c-F104 | 0.97 | ні / так |
| 4 | Що таке тариф T-1? | vector | c-tariff-T2 | c-tariff-T1 | 1.18 | ні / так |
| 5 | Що сталося зі знахідкою F-101? | baseline (keyword) | t-findings | c-F101 | 1.01 | ні / так |

Плюс контракт `TestRerank_ChangesOrder`: на «яка ставка комісії на тарифі T-2?» чанк про T-1 (`c-A331-rate`, score
першого етапу 0.88) опускається нижче `c-A114-rate`. top1_relevance для цих п'яти: до — 1–2, після — 5.

На `embeddinggemma` приклади 3 і 4 вже правильні на першому етапі (модель розрізняє F-101/F-104 і T-1/T-2 краще за
стаб), тож rerank там нічого не міняє, а 1, 2, 5 — міняє так само. Чесний мінус: на «Хто аудитор Northwind Cloud?»
baseline дає правильну таблицю `t-vendors`, а rerank піднімає `c-availability` (там теж Northwind, але без аудитора) —
ознаки не бачать, що відповідь у стовпчику таблиці.

**Калібрування quality gate.** Прогнав усі 10 single-hop запитів gold set через пул + rerank (`TestQualityGate`):
мінімальний score in-domain — 0.70 («Який період охоплює огляд SOC2?»), out-of-domain «Яка погода в Києві?» —
0.27. Поріг 0.55 — приблизно посередині, з запасом в обидва боки. Тест падає, якщо хоч один in-domain запит
опиниться нижче або out-of-domain — вище.

## 4. Семантичний кеш

`cache_check` — перший вузол графа. Hit іде одразу у `cached`, miss — у `classify`.

**Що кешую:** фінальну відповідь разом із цитатами (provenance зберігається, `[cache_hit=true]` + ті самі chunk_id).
Проміжні `RetrievalResult` не кешую: вони дешеві (локальні індекси), а найдорожче в production — генерація LLM.
Відповіді `insufficient` не кешуються, щоб відмова не «залипла» після оновлення корпусу.

**Ключ і межі:**
- `tenant` + `policy` (permission boundary) + `corpus_version` — запис видно лише в тому самому scope. У консолі
  tenant один (`ledgerworks/legal`); у web-режимі його треба брати з користувача сесії — інакше, як і в стартері,
  відповідь одного користувача бачать усі.
- `corpus_version` — sha256 від id + текстів чанків. Новий `chunks.json` → нова версія → старі записи не збігаються,
  а `invalidate(version)` їх видаляє. TTL = 24 год.
- Hit можливий лише тоді, коли збігаються **коди сутностей** запиту (T-2, CT-2025-031, A-114…) і **тип питання**
  (ставка / підписант / мерчанти / знахідки / «про що»), і тільки потім — cosine ≥ поріг.

**Чому самого cosine мало** (виміряно; guard = коди й тип збігаються):

| Пара | Має влучити? | стаб | embeddinggemma | guard |
|---|---|---:|---:|---|
| «Що таке тариф T-2?» ≈ «Розкажи про тарифний план T-2» | так | 0.47 | 0.84 | так |
| «Скільки коштує тариф T-2?» ≈ «Яка ціна тарифу T-2?» | так | 0.42 | 0.93 | так |
| «Хто підписав договір CT-2025-031?» ≈ «Хто є підписантом договору CT-2025-031?» | так | 0.78 | 0.96 | так |
| «Що таке тариф T-2?» vs «Що таке тариф T-1?» | ні | 0.88 | 0.78 | ні |
| «Яка ставка комісії на тарифі T-2?» vs «Які мерчанти на тарифі T-2?» | ні | 0.54 | 0.60 | ні |
| «Хто підписав договір CT-2025-031?» vs «…CT-2025-014?» | ні | 0.91 | 0.86 | ні |

На обох ембедингах негативна пара має **вищий** cosine, ніж частина перефразувань (0.91 / 0.86 проти 0.42 / 0.84).
Жоден поріг тут не працює без guard-а. З guard-ом пороги такі: стаб — 0.40 (нижче мінімального перефразування 0.42),
`embeddinggemma` — 0.80 (нижче 0.84). 0.92 зі стартової гіпотези на `embeddinggemma` пропустив би перше
перефразування. False-positive на цих 3 негативних парах + ще одній (`TestCache_NoFalsePositive`) — 0 з 4,
hit на перефразуваннях — 3 з 3.

**Демонстрація** (`go run . console`, стаб):

```text
> Скільки коштує тариф T-2?   → [cache_hit=false] Топ-результат (c-tariff-T2, ledgerworks_soc2): Тариф T-2 — … 2.9 % …   (miss)
> Яка ціна тарифу T-2?        → [cache_hit=true]  Топ-результат (c-tariff-T2, ledgerworks_soc2): …                        (semantic hit)
> Яка ціна тарифу T-1?        → [cache_hit=false] Топ-результат (c-tariff-T1, ledgerworks_soc2): Тариф T-1 — … 1.8 % …   (miss, інший код)
> Скільки коштує тариф T-2?   → [cache_hit=true]  …                                                                       (exact hit)
```

Лічильники кешу на цій сесії: exact 1, semantic 1, miss 3 (з запитом Оксани першим). `TestGraph_CacheParaphrase`
перевіряє те саме через граф: 1 semantic hit, 2 miss.

## 5. Eval harness: vector-RAG vs Agentic

20 запитів (10 single-hop, 10 multi-hop) — `goldSet` в `eval.go`. Для кожного: правильні чанки (перший — найкращий)
і факти, які має містити повна відповідь.

- **Vector-RAG baseline:** один пошук ембедингами top-5 → «читач». LLM у мене немає, тому читач ідеальний: кожен факт,
  що є в top-5, вважається відповіддю з цитатою. Це **на користь** baseline.
- **Agentic:** повний граф із порожнім кешем.
- **top1_relevance (1–5):** рубрика — 5 найкращий gold-чанк, 4 інший gold-чанк, 1 нерелевантний; out-of-domain: 5 за
  відмову. Рахується автоматично; кожен рядок видно в `eval.md`, його легко перевірити очима. Для multi-hop «топ-1» — найкращий процитований
  чанк (у такої відповіді немає одного топ-1).
- **groundedness:** частка потрібних фактів, які є у відповіді **і** в тексті процитованого чанка.
- **latency_p95:** кожен запит 5 разів, медіана; потім p95 по запитах.
- **tokens_used:** слова в контексті, який пішов би в LLM (запит + процитовані чанки) — однаково для обох систем.

**Стаб-ембединги** (`go test`, відтворюється без мережі):

| Система | Тип | top1_relevance | groundedness | latency_p95 | tokens_used |
|---|---|---:|---:|---:|---:|
| vector-RAG | single | 4.10 | 0.90 | 18µs | 95 |
| agentic | single | 4.90 | 1.00 | 1.1ms | 74 |
| vector-RAG | multi | 1.90 | 0.60 | 19µs | 86 |
| agentic | multi | 5.00 | 1.00 | 1.2ms | 43 |

**embeddinggemma** (Ollama, CPU):

| Система | Тип | top1_relevance | groundedness | latency_p95 | tokens_used |
|---|---|---:|---:|---:|---:|
| vector-RAG | single | 4.60 | 0.90 | 145ms | 104 |
| agentic | single | 5.00 | 1.00 | 476ms | 74 |
| vector-RAG | multi | 3.10 | 0.82 | 117ms | 109 |
| agentic | multi | 5.00 | 1.00 | 459ms | 43 |

Справжня модель підтягує vector-RAG (multi-hop top1 3.1 замість 1.9), але groundedness на multi-hop усе одно 0.82:
підписант і мерчант лежать у різних чанках, і в top-5 часто потрапляє лише один. Agentic повільніший у ~3 рази:
кеш і rerank теж ембедять запит. Перший прогін був 3–4 с, бо rerank ембедив усі 20 кандидатів; після того як
rerank став брати вектори чанків з індексу, лишився один виклик моделі на запит у кожному вузлі.

Повна таблиця по 20 запитах (стаб) — у [`eval.md`](eval.md), генерується командою вище.

**Висновки:**
- **Multi-hop — тут agentic виграє однозначно.** Vector-RAG на «хто підписав договір мерчанта A-207» знаходить чанк
  мерчанта, але не підписанта: ці факти в різних чанках, а між ними — лише номер договору. top-5 часто забиває
  чанками про тарифи. Graph traversal іде по ребрах і не залежить від схожості тексту.
- **Single-hop із кодами в запиті** (CT-2025-031, F-104) — vector-RAG майже достатній, але плутає сусідні коди
  (F-101 замість F-104). Тут вистачає re-ranker-а, граф не потрібен.
- **Out-of-domain** — vector-RAG завжди щось повертає; agentic каже «недостатньо доказів» завдяки quality gate.
- **Ціна:** agentic повільніший (кеш, 4 гілки, rerank) — на стабі мілісекунди, на `embeddinggemma` latency
  визначає кількість викликів ембедингів. Зате контекст для LLM менший (43 слова проти 86 на multi-hop): у відповідь
  ідуть лише процитовані чанки, а не весь top-5.
- Корпус маленький (21 чанк), тож усі цифри — про поведінку, а не про якість на 5 000 PDF.

## 6. Decision sheet: vector vs GraphRAG vs agentic

| | |
|---|---|
| **Корпус** | звіти SOC2 / PCI DSS, договори з еквайрами, вимоги НБУ. Багато кодів (тарифи, договори, мерчанти, знахідки), таблиці, зв'язки між документами |
| **Типові запити** | юристи й комплаєнс: «хто підписав», «хто підпадає під вимогу», «які знахідки High у вендора» — фільтр + 1–3 hop-и |
| **Щабель зараз** | **Agentic GraphRAG** (hybrid retrieval → graph → planner із budget) |
| **Чому не vector** | eval: на multi-hop groundedness 0.60 проти 1.00, top1 1.9 проти 5.0. Відповідь розкидана по чанках, зв'язок — лише код договору, а не схожий текст |
| **Чому не просто GraphRAG без планувальника** | частина запитів — чистий single-hop або семантика без кодів («які інциденти недоступності»). Планувальник вирішує, чи потрібен граф, і обмежує кількість кроків |
| **Що достатньо, якщо запити прості** | hybrid (BM25 + vector + RRF) + re-ranker: на single-hop він дає ті самі 1.00 groundedness, і граф не потрібен |
| **Наступний щабель** | якщо з'являться запити «по всьому архіву» (тенденції, підсумки за рік) — global search по спільнотах графа (Microsoft GraphRAG) і LLM-класифікатор/декомпозитор замість regex. Якщо корпус часто змінюється — event-sourced стан і LLM-Wiki / OKF (Вебінар 9) як третя частина KB поруч із таксономією та hybrid retrieval |
| **Ризики** | regex-граф ламається на нових шаблонах тексту → потрібні сутності з ДЗ 5; offline reranker на ознаках — заміна cross-encoder-а лише поки немає моделі |

FalkorDB GraphRAG-SDK не використовував — граф in-memory, як і в ДЗ 5.

## ++ Advanced: паралельний hybrid + RRF

Зроблено як частину графа: `keyword_search` (Okapi BM25, k1 = 1.2, b = 0.75) і `vector_search` ідуть паралельно через
`AddFanOut`, зливаються через `JoinNode` і RRF (k = 60) перед rerank (`fuse` у `rerank.go`). Скласти скори напряму
не можна: BM25 не має верхньої межі, cosine — від 0 до 1, тож сума завжди віддає перемогу
BM25. RRF бере лише ранги. `TestFuse_RRF`: чанк, що є в обох списках, виграє в чанка з BM25 = 900, який є тільки в
одному. Приклад користі — запит 2 у таблиці rerank: BM25 ставить першим `c-sign-014` (той самий шаблон речення), а
vector — `c-sign-031`; RRF + rerank дають правильний.

LLM-суддю для groundedness не робив (немає ключа) — groundedness рахується детерміновано по span-ах.
