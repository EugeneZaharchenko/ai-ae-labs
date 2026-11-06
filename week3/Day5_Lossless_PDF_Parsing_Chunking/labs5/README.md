# Лабораторна 5 — ingestion-конвеєр: PDF → Markdown → ієрархічні чанки

**Станом на 09/2026:** Go 1.27.1, ADK Go v2.5.0, docling-mcp 3.2.1. Жодного LLM, ключа або `.env`.
Завдання — у [Homework.md](Homework.md).

## ДЗ 5 — що зроблено

Станом на 10/2026: Go 1.27.1, ADK Go v2.5.0, docling-mcp 3.2.1 (встановлено через `uv tool install`, CPU-версія torch).
Без LLM, без API-ключа, без `.env`.

### Що я реалізував

| Файл | Що робить |
|---|---|
| `chunk.go` | вузол `chunk`: Parent-Child. Parent — заголовок розділу 1-го рівня (`2 Findings`) або заголовок без номера. `2.1`, `1.2.1` новий parent **не** відкривають, лише доповнюють `section_path` — бо docling усі заголовки друкує як `##`, і рівень видно тільки з номера. Таблиця — завжди рівно один child. Довгий текст ріжеться по реченнях до 200 токенів |
| `entities.go` | вузол `extract_entities`: сутності беру лише з таблиць (там структура вже є): Vendor, Finding, Severity, Report, Merchant, Contract…, а для NIST — Category / Subcategory (`GOVERN 1.1`). У кожної — `source_chunk_id` |
| `pipeline.go` | `index_vector` (кожен child → vecindex, parents не індексуються), `index_graph` (вузли + ребра з одного рядка таблиці: `has_finding`, `has_severity`, `has_report`, `has_subcategory`…), `report` (статистика + експорт `chunks.json`) |
| `query.go` | приклад multi-hop запиту по графу (див. нижче) |
| `main_test.go` | табличні тести на `StrictContextMock` для кожного вузла; контракт `TestChunk_TableStaysWhole` без skip |

#### ++ Advanced A1: векторний індекс + 5 запитів по таблицях

Індекс — `internal/vecindex`, **deterministic stub embedding** (hashed bag of words, 128 вимірів), не семантична модель.
Індексуються лише children; після пошуку `retrieve()` (`query.go`) довантажує parent за `parent_id`.
Запити йдуть по `testdata/ledgerworks_soc2.chunks.json` (там таблиці з числами). Перевіряє `TestA1_TableQueries`,
вивід — `go test -v -run TestA1 .`. Відповідь я рахував вручну з таблиці, модель тут не потрібна.

| # | Запит | Топ-3 children (score) | Відповідь (вручну з таблиці) |
|---|---|---|---|
| 1 | `Fee % for Merchant A-114 on Tariff T-2` | **p4.c1** table (0.703), p5.c1 text (0.174), p3.c2 text (0.142) | **2.9 %**, контракт CT-2025-014 |
| 2 | `Finding ID Severity High Vendor Acme Bank JSC` | **p3.c1** table (0.591), p5.c1 text (0.408), p3.c2 text (0.333) | **F-101** (CC6.1, Open), **F-104** (CC8.1, In progress) |
| 3 | `which Vendor has Report SOC2 Type I and which Auditor` | **p2.c3** table (0.464), p4.c2 list (0.298), p2.c1 text (0.241) | **Globex Payments**, аудитор Kovalenko & Partners |
| 4 | `Control ID and Status of finding F-103 Northwind Cloud` | p2.c3 table (0.464), p4.c2 list (0.464), **p3.c1** table (0.457) | **CC6.6, Open** (Severity High) |
| 5 | `which Merchant has Tariff T-1 with Fee 1.8 and which Contract` | **p4.c1** table (0.511), p3.c1 table (0.222), p4.c2 list (0.158) | **A-331**, CT-2025-007 (з p4.c2: підписав Олег Мельник, 20.01.2025) |

Parent-контекст, який отримала б модель (parent → його children за `parent_id`):

- запити 1 і 5 → parent **p4** `3 Tariffs and Signers`, child `p4.c1` (`3 Tariffs and Signers / 3.1 Merchant tariffs`):
  ```
  | Merchant   | Tariff   |   Fee, % | NBU 2026 requirement   | Contract    |
  |------------|----------|----------|------------------------|-------------|
  | A-114      | T-2      |      2.9 | Yes                    | CT-2025-014 |
  | A-207      | T-2      |      2.9 | Yes                    | CT-2025-031 |
  | A-331      | T-1      |      1.8 | No                     | CT-2025-007 |
  ```
  Це саме приклад із лекції: 1.8 — ставка **A-331**, а не A-114. Таблиця ціла, із заголовком `Fee, %`, тому рядки не плутаються.
- запити 2 і 4 → parent **p3** `2 Findings`, child `p3.c1` (`2 Findings / 2.1 Security Findings`): таблиця
  `| Finding ID | Vendor | Control ID | Severity | Status |` з усіма 5 рядками F-101 … F-105.
- запит 3 → parent **p2** `1 Scope`, child `p2.c3` (`1 Scope / 1.1 Vendors in scope`): таблиця
  `| Vendor | Service | Report | Period | Auditor |` з 3 вендорами.

Що я помітив:
- У всіх 5 запитах правильна таблиця в топ-3 і вона **ціла** — тест перевіряє, що в ній є і заголовок, і потрібні значення.
- Запит 4 — найслабший: правильна таблиця лише #3 (0.457), бо таблиця вендорів теж містить `Northwind Cloud`. Stub рахує
  збіг слів, а не сенс.
- На NIST stub гірший: для `GOVERN 6 third-party software data supply chain subcategories` потрібний шматок таблиці
  `p16.c12` лише **#5** (0.282) — довга таблиця (~300 слів) «розмиває» вектор, і короткі абзаци зі словом GOVERN виграють.
  Зафіксовано в `TestA1_NISTTableRank`. Справжня модель (`internal/embed` з `OLLAMA_EMBED_MODEL`) або reranking (ДЗ 6) — наступний крок.

## Запуск

```bash
cd week3/Day5_Lossless_PDF_Parsing_Chunking/labs5
go test ./...                                   # без docling і без мережі

# PDF через docling (перший запуск качає моделі; як я його встановив — у «Обмеженнях»)
export DOCLING_MCP_CMD="$HOME/.local/bin/docling-mcp-server --transport stdio"
CHUNKS_OUT=testdata/chunks.json go run . console     # ввести: testdata/nist_ai_rmf.pdf
```

`CHUNKS_OUT` — куди зберегти чанки (JSON-масив `Chunk`, формат для ДЗ 6).

### Тестовий документ курсу (`ledgerworks_soc2.md`)

```
Blocks: 21 (3 tables, 11 headings, 6 paragraphs, 1 lists)
Parents: 5, Children: 10 (tables: 3)
Children size: n=10, tokens min/avg/max = 12/32.0/78
Tables: 3/3 preserved
Naive 800-token windows: 1 chunks, tables whole: 3/3
Entities: 29, vectors_added: 10, nodes_added: 26, edges_added: 27
```

`vectors_added` = кількості children (10), `nodes_added` (26) ≤ entities (29): Acme Bank JSC є у двох таблицях — це один вузол із двома `sources`, а не два.
Документ маленький (≈500 слів), тому naive-вікно на 800 токенів тут нічого не ріже — порівняння показую на своєму PDF нижче.

### Мій документ: NIST AI RMF 1.0 (`testdata/nist_ai_rmf.pdf`)

[NIST AI 100-1](https://doi.org/10.6028/NIST.AI.100-1) — фреймворк керування ризиками AI (тема AI + безпека), 48 сторінок, public domain.
3 рівні заголовків (`1. Framing Risk` → `1.2 Challenges…` → `1.2.1 Risk Measurement`) і 4 великі таблиці
(Table 1–4: категорії й підкатегорії функцій GOVERN / MAP / MEASURE / MANAGE). Текстовий PDF, OCR не потрібен.

Прогін через docling (CPU, ~2 хв), event log вузла `report`:

```
$ CHUNKS_OUT=testdata/chunks.json go run . console      # ввести: testdata/nist_ai_rmf.pdf
Blocks: 253 (13 tables, 38 headings, 189 paragraphs, 13 lists)
Parents: 22, Children: 219 (tables: 13)
Children size: n=219, tokens min/avg/max = 1/72.3/628
Tables: 13/13 preserved
Naive 800-token windows: 21 chunks, tables whole: 9/13
Entities: 97, vectors_added: 219, nodes_added: 91, edges_added: 72
Chunks saved to testdata/chunks.json
```

| Метрика | Значення |
|---|---|
| Parents (рівень 0) | 22 |
| Children (рівень 2) | 219, з них таблиць 13 |
| Розмір children, токенів | min 1 / avg 72.3 / max 628 (max — таблиця змісту; таблиці ліміт 200 не ріжуть) |
| Таблиці цілі | **13/13** (structural) проти **9/13** (naive по 800) |
| Граф | 91 вузол = 19 категорій + 72 підкатегорії — рівно стільки, скільки в NIST AI RMF; 72 ребра `has_subcategory` |
| `vectors_added` | 219 = кількості children |

Чому 13 таблиць, а не 4: docling ріже кожну таблицю на межі сторінки — Table 1–4 стали **11 шматками**,
кожен зі своїм рядком заголовків `| Categories | Subcategories |` (для retrieval це навіть добре — кожен шматок самодостатній).
Ще 2 «таблиці» — це зміст і підпис рисунка Fig. 3, який docling помилково розпізнав як таблицю.
Тобто `13/13` означає: усе, що витяг парсер, chunker зберіг цілим. Що парсер побачив саме те, що на сторінці, я перевірив
вручну: усі 72 підкатегорії (`GOVERN 1.1` … `MANAGE 4.3`) є в тексті, і тест `TestNIST_RealDoclingOutput` це фіксує.

Проблема, яку знайшов: перший рядок шматка після розриву сторінки часто має порожню категорію
(або обрізок тексту `that considers and communicates AI risk.`), і зв'язок «підкатегорія → категорія» губився (56 ребер з 72).
Виправив у `tableReader`: остання категорія переноситься на наступний табличний чанк того ж розділу → 72/72.

### Naive chunking vs структурний — та сама таблиця

Шматок Table 1 (GOVERN 4–6).

**(а) Naive, вікна по 800 токенів** (`chunkeval.NaiveWindows`, Markdown сплющено в слова). Таблиця розрізана між вікнами 11 і 12:

```
вікно 11 (кінець): … GOVERN 5.2: Mechanisms are established to enable the team that developed or deployed
                   AI systems to regularly incorporate adjudicated feedback … | | GOVERN 6: Policies and
                   procedures are in place to address AI risks and benefits arising from third-party
вікно 12 (початок): software and data and other supply chain issues. | GOVERN 6.1: Policies and procedures
                   are in place that address AI risks associated with third-party entities, … GOVERN 6.2: …
```

Вікно 12 починається з середини клітинки, без рядка `| Categories | Subcategories |`, рядки таблиці злиплися в одну стрічку.
Retriever, який поверне вікно 12, дасть моделі `GOVERN 6.1` без назви категорії — і модель «домалює» її сама (той самий leaky RAG).

**(б) Структурний парсинг** — child `p16.c12` (parent `p16`), одна ціла Markdown-таблиця із заголовками:

```
| Categories                                                    | Subcategories                                          |
|---------------------------------------------------------------|--------------------------------------------------------|
| that considers and communicates AI risk.                      | GOVERN 4.2: Organizational teams document the risks …  |
| GOVERN 5: Processes are in place for robust engagement with … | GOVERN 5.1: Organizational policies and practices …    |
| GOVERN 6: Policies and procedures are in place to address AI  | GOVERN 6.1: Policies and procedures are in place …     |
|   risks … third-party software and data and other supply …    |   GOVERN 6.2: Contingency processes are in place …     |
```

(клітинки скорочено для README; у `testdata/chunks.json` — повний текст).
Рахує це сам `report`: `Tables: 13/13 preserved` проти `Naive 800-token windows: … tables whole: 9/13`.

### Експорт для ДЗ 6

- `testdata/chunks.json` — чанки NIST AI RMF (241 чанк).
- `testdata/ledgerworks_soc2.chunks.json` — чанки тестового PDF курсу (через docling: 5 parents, 12 children, 3/3 таблиці).

### Обмеження пайплайна

- **Таблиці на кілька сторінок** docling віддає шматками; я їх не склеюю, а лише переношу категорію між шматками. Текст клітинки, розірваний сторінкою (`GOVERN 4: … culture` / `that considers …`), лишається у двох чанках.
- **Хибні таблиці**: зміст і підпис рисунка Fig. 3 docling розпізнав як таблиці (рядки рисунка перемішані). Сутностей з них не беру — немає колонок, які я знаю.
- **Заголовки без номера** всередині розділу (`The GOVERN function:`) стають окремим parent — бо без номера рівень невідомий. Через це `section_path` таблиць GOVERN — `The GOVERN function:`, а не `5. AI RMF Core / 5.1 Govern`.
- **Дрібні children** (min 1 токен) — короткі рядки на кшталт номерів сторінок, які docling лишає як абзаци.
- **Сутності — лише з таблиць і лише з відомих колонок** (`entityColumns`). З вільного тексту нічого не витягується (це вже LLM-екстракція, бонус-трек).
- **Ембединг — deterministic stub** (`internal/vecindex`), не семантичний. Граф і індекс — in-memory: рестарт = все зникає.
- **Скановані PDF** не перевіряв (OCR вимкнено, `DOCLING_MCP_DO_OCR=false`).
- **docling на моєму ноуті**: `uvx` не було в PATH, а `uv tool run` упав на завантаженні CUDA-пакетів (timeout). Поставив CPU-версію torch і Python 3.12
  (на 3.14 сервер падав на pydantic): `uv tool install --python 3.12 'docling-mcp[local]==3.2.1' --index https://download.pytorch.org/whl/cpu --index-strategy unsafe-best-match`,
  далі `DOCLING_MCP_CMD="$HOME/.local/bin/docling-mcp-server --transport stdio"`.

### Приклад запиту

Запит юриста: *«які Findings з Severity=High у Vendor=Acme у звітах SOC2 Type II?»* — це multi-hop, вектор його не пройде:

```
vendor:acme-bank-jsc -has_report-> report:soc2-type-ii        (перевірка, що звіт саме Type II)
vendor:acme-bank-jsc -has_finding-> finding:f-101 -has_severity-> severity:high   ✔
vendor:acme-bank-jsc -has_finding-> finding:f-102 -has_severity-> severity:medium
vendor:acme-bank-jsc -has_finding-> finding:f-104 -has_severity-> severity:high   ✔
```

Відповідь: **F-101, F-104** — збігається з розділом «4 Conclusion» документа. Перевіряє `TestIndexGraph_AcmeHighFindings` (функція `highFindings` у `query.go`); для Globex (SOC2 Type I) результат порожній.
Кожен вузол і ребро має `source` — id чанка-таблиці, тож аудитор бачить, з якого рядка взято факт.

## Запуск

```bash
cd week3/Day5_Lossless_PDF_Parsing_Chunking/labs5
go run . console          # введіть: testdata/ledgerworks_soc2.md
go test -v .              # 9 passed, 1 skipped — контракт TestChunk_TableStaysWhole
```

Граф (`graph.go`):

```text
Start → load → parse → chunk → extract_entities → index_vector → index_graph → report
```

| Файл | Що в ньому | Ваша робота |
|---|---|---|
| `pipeline.go` | вузли й типи `Source`, `Document`, `Chunk`, `Entity`, `Batch`, `Stores` | `TODO(студент)`: `chunk`, `extract_entities`, `index_*`, `report` |
| `graph.go` | топологія | ++ A3: fan-out на кілька документів |
| `main.go` | лише wiring: docling-клієнт + launcher | — |
| `main_test.go` | табличні тести на `StrictContextMock` | зняти `t.Skip`, переписати тести-заглушки |
| `internal/docling` | MCP-клієнт до docling-mcp | читати, не змінювати |
| `internal/embed` | ембединги (чистий Go за замовчуванням, Ollama — через `OLLAMA_EMBED_MODEL`), `Cosine`, `Rerank` | у `index_vector`, A1 |
| `internal/mdblocks` | Markdown → блоки (`Document.Blocks`); таблиця — один блок; глибина заголовка з нумерації, а не з `#` | у `chunk`, `extract_entities` |
| `internal/chunkeval` | токени, min/avg/max, скільки таблиць цілі, naive baseline | у `report`, порівняння для README |
| `internal/vecindex` | in-memory індекс зі стаб-ембедингом | у `index_vector`, A1 |
| `internal/kgraph` | in-memory граф із provenance | у `index_graph` |
| `testdata/` | тестовий документ: `.md`, `.pdf` і реальний вихід docling `.docling.md` | додати свій PDF |

## PDF через docling-mcp

`parse` передає PDF у [docling-mcp](https://github.com/docling-project/docling-mcp) — MCP-сервер над
[docling](https://github.com/docling-project/docling), який розпізнає розмітку сторінки й таблиці. Клієнт —
офіційний Go SDK `github.com/modelcontextprotocol/go-sdk` через stdio; два виклики на документ:

```text
convert_document_into_docling_document(source)  → document_key
export_docling_document_to_markdown(document_key) → markdown
```

**Що потрібно:** [`uv`](https://docs.astral.sh/uv/getting-started/installation/) (у dev-контейнері вже є). Сервер
стартує сам, лише коли приходить перший PDF:

```bash
uvx --from='docling-mcp[local]' docling-mcp-server --transport stdio
```

Перший запуск завантажує docling і моделі розмітки (сотні MB, кілька хвилин; прогрес — у stderr).

| Змінна | За замовчуванням | Навіщо |
|---|---|---|
| `DOCLING_MCP_CMD` | команда вище | свій сервер або пін версії: `uvx --from=docling-mcp[local]==3.2.1 docling-mcp-server --transport stdio` |
| `DOCLING_MCP_CONVERSION_MODE` | `local` | `remote` + `DOCLING_MCP_SERVICE_URL` — конвертація через Docling Serve |
| `DOCLING_MCP_DO_OCR` | `false` | `true` для сканованого PDF без текстового шару (завантажить OCR-моделі) |

Перевірити справжній сервер на тестовому PDF (з кореня репозиторію):

```bash
DOCLING_E2E=1 go test -run TestRealServer -v ./week3/Day5_Lossless_PDF_Parsing_Chunking/labs5/internal/docling
```

Звичайний `go test ./...` docling не запускає: PDF-шлях у тестах лаби йде через фейковий конвертер.

## Що видно на тестовому PDF

`testdata/ledgerworks_soc2.docling.md` — справжній вихід docling для `ledgerworks_soc2.pdf`:

- **3/3 таблиці цілі**, із заголовками стовпчиків — lossless-частина працює.
- **Рівні заголовків сплющено**: і `1 Scope`, і `1.1 Vendors in scope` стають `##`. Parent-Child
  ієрархію доведеться відновлювати з нумерації розділів, а не з кількості `#`.

## Експорт для ДЗ 6

ДЗ 6 читає масив `Chunk` у JSON (поля фіксує `TestChunkJSONContract`). Збережіть свої чанки й
запустіть ДЗ 6 так: `CHUNKS=…/chunks.json go run . console`.
