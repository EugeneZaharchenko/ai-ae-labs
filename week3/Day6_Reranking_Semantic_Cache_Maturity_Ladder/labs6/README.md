# Лабораторна 6 — retrieval: пошук → re-rank → відповідь із provenance + кеш

**Станом на 09/2026:** Go 1.27.1, ADK Go v2.4.0. Жодного LLM, ключа або `.env` для стартера.
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

## Кеш

Кеш — поле `retriever`, під `sync.Mutex`: коли ви зробите fan-out, вузли працюватимуть паралельно,
і `TestCache_ConcurrentAccess` під `-race` це ловить. Ключ зараз — нормалізований запит (точний
hit). Семантичний кеш, ізоляція tenant/версії корпусу й TTL — Homework п.4.
