// Скелет тестів для ДЗ 6.
//
// Homework.md вимагає зелений `go test ./...`; цей файл дає готову форму, щоб
// перші хвилини пішли на retrieval, а не на «як підсунути agent.Context».
//
// ЯК ЦИМ КОРИСТУВАТИСЬ
//
//	go test ./...            — зараз зелено: тести описують ЗАГЛУШКИ стартера
//	                           і baseline-пошук по testdata/chunks.json.
//	                           Зелений старт означає, що середовище живе.
//
// Далі ви реалізуєте TODO у retrieval.go / graph.go, тести падають (бо
// описують стару поведінку) — і ви переписуєте їх під свою. Місця позначені
// TODO(студент).
//
// Два тести нижче — НЕ заготовки, а контракти ДЗ, і вони під t.Skip доти,
// доки ви не візьметесь за відповідний пункт:
//
//	TestRerank_ChangesOrder      — критерій «5 запитів до/після» (15 балів)
//	TestCache_ParaphraseHits     — критерій «hit на перефразованому» (10 балів)
//
// Кожен тест будує СВІЙ retriever (newTestRetriever) — кеш не глобальний, тож
// тести не ділять стан і можуть іти паралельно.
//
// Чому StrictContextMock, а не nil: вузли приймають agent.Context. nil працює
// рівно доти, доки ви його не торкаєтесь; щойно у вузлі з'явиться
// ctx.InvocationID() — nil дасть panic там, де його ніхто не чекає.
// StrictContextMock панікує гучно і одразу на будь-якому непередбаченому
// методі, тож помилка ловиться в тесті, а не на демо.
package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/corpus"
)

type nodeCtx struct {
	agent.StrictContextMock
}

func newNodeCtx() *nodeCtx {
	return &nodeCtx{StrictContextMock: agent.NewStrictContextMock(context.Background())}
}

func newTestRetriever(t *testing.T) *retriever {
	t.Helper()
	chunks, err := corpus.Load(defaultChunks)
	if err != nil {
		t.Fatalf("корпус: %v", err)
	}
	return newRetriever(chunks)
}

// --- Вузол search ------------------------------------------------------------

func TestSearch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		query   string
		wantErr bool
		wantTop string // "" — будь-який кандидат
		wantN   int
	}{
		{name: "тариф T-2", query: "Що таке тариф T-2?", wantTop: "c-tariff-T2", wantN: 1},
		{name: "знахідка за ID і суттю", query: "F-101 привілейовані облікові записи", wantTop: "c-F101", wantN: 1},
		{name: "нічого спільного", query: "рецепт борщу", wantN: 0},
		{name: "порожній запит — помилка", query: "  ", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := newTestRetriever(t).search(newNodeCtx(), tt.query)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("search(%q) error = nil; очікували помилку", tt.query)
				}
				return
			}
			if err != nil {
				t.Fatalf("search(%q) error = %v", tt.query, err)
			}
			if len(got.Candidates) < tt.wantN {
				t.Fatalf("кандидатів = %d, очікували щонайменше %d", len(got.Candidates), tt.wantN)
			}
			if len(got.Candidates) > topK {
				t.Errorf("кандидатів = %d, більше за topK=%d", len(got.Candidates), topK)
			}
			if tt.wantTop != "" && got.Candidates[0].ChunkID != tt.wantTop {
				t.Errorf("топ-1 = %q, очікували %q", got.Candidates[0].ChunkID, tt.wantTop)
			}
			for _, c := range got.Candidates {
				if c.DocumentID == "" {
					t.Errorf("кандидат %q без document_id — provenance втрачено", c.ChunkID)
				}
			}
			if got.CacheHit {
				t.Errorf("CacheHit = true на порожньому кеші")
			}
		})
	}
}

// --- Ранній win із лекції ----------------------------------------------------

// TestPipeline_EarlyWin проганяє повний граф search → rerank → answer так
// само, як `go run . console`, тільки відтворювано в CI.
//
// Перевіряємо provenance: у відповіді є chunk_id і document_id джерела.
// Відповідь без посилання на джерело в цьому курсі не зараховується.
func TestPipeline_EarlyWin(t *testing.T) {
	t.Parallel()
	a, err := newGraph(newTestRetriever(t))
	if err != nil {
		t.Fatalf("newGraph: %v", err)
	}
	res, err := labrun.Run(context.Background(), a, "Що таке тариф T-2?")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{"c-tariff-T2", "ledgerworks_soc2", "2.9"} {
		if !strings.Contains(res.Final, want) {
			t.Errorf("у відповіді немає %q:\n%s", want, res.Final)
		}
	}
}

func TestAnswer_NoCandidates(t *testing.T) {
	t.Parallel()
	out, err := newTestRetriever(t).answer(newNodeCtx(), SearchResult{Query: "щось"})
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if !strings.Contains(out, "Нічого не знайдено") {
		t.Errorf("порожній набір кандидатів має давати явну відмову, отримали %q", out)
	}
}

// --- Кеш ---------------------------------------------------------------------

// TestCache_ExactHit фіксує поточну поведінку: кеш точний, ключ —
// нормалізований запит (регістр і пунктуація не важать).
func TestCache_ExactHit(t *testing.T) {
	t.Parallel()
	r := newTestRetriever(t)
	ctx := newNodeCtx()

	first, err := r.search(ctx, "Що таке тариф T-2?")
	if err != nil {
		t.Fatalf("search #1: %v", err)
	}
	if first.CacheHit {
		t.Fatal("перший запит не може бути hit")
	}
	if _, err := r.answer(ctx, first); err != nil {
		t.Fatalf("answer: %v", err)
	}

	second, err := r.search(ctx, "що таке тариф t-2")
	if err != nil {
		t.Fatalf("search #2: %v", err)
	}
	if !second.CacheHit {
		t.Fatal("повтор того самого запиту мав дати cache hit")
	}
	out, err := r.answer(ctx, second)
	if err != nil {
		t.Fatalf("answer #2: %v", err)
	}
	if !strings.Contains(out, "[cache_hit=true]") || !strings.Contains(out, "c-tariff-T2") {
		t.Errorf("hit має повернути закешовану відповідь із provenance, отримали %q", out)
	}
}

// TestCache_ConcurrentAccess — кеш безпечний під -race: fan-out і web-режим
// викликають вузли одночасно.
func TestCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	r := newTestRetriever(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			ctx := newNodeCtx()
			res, err := r.search(ctx, "Що таке тариф T-2?")
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := r.answer(ctx, res); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

// TestCache_ParaphraseHits — КОНТРАКТ ДЗ (10 балів), не заготовка.
//
// Точний кеш ловить лише буквальний повтор. Вимога ДЗ — семантичний кеш:
// перефразований запит теж має влучати.
//
// TODO(студент): приберіть t.Skip, коли реалізуєте схожість замість map-lookup.
// І одразу додайте ДРУГИЙ тест — на false positive: два РІЗНІ за змістом
// запити (напр. про T-2 і про T-1), які не мають ділити відповідь. Високий
// hit-rate без виміряного false-positive rate — це не перемога, а регресія
// якості («кеш — це межа безпеки»).
func TestCache_ParaphraseHits(t *testing.T) {
	t.Skip("зніміть skip, коли реалізуєте семантичний кеш замість точного map-lookup")

	r := newTestRetriever(t)
	ctx := newNodeCtx()

	first, err := r.search(ctx, "Що таке тариф T-2?")
	if err != nil {
		t.Fatalf("search #1: %v", err)
	}
	if _, err := r.answer(ctx, first); err != nil {
		t.Fatalf("answer: %v", err)
	}

	para, err := r.search(ctx, "Розкажи про тарифний план T-2")
	if err != nil {
		t.Fatalf("search перефразованого: %v", err)
	}
	if !para.CacheHit {
		t.Errorf("перефразований запит не влучив у семантичний кеш")
	}
}

// --- Re-ranking --------------------------------------------------------------

// TestRerank_Stub фіксує поточну поведінку: заглушка не змінює порядок.
//
// TODO(студент): цей тест має впасти, щойно ви реалізуєте re-ranker.
func TestRerank_Stub(t *testing.T) {
	t.Parallel()
	in := SearchResult{Query: "q", Candidates: []Candidate{
		{ChunkID: "a", Score: 0.1},
		{ChunkID: "b", Score: 0.9},
	}}
	got, err := newTestRetriever(t).rerank(newNodeCtx(), in)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if got.Candidates[0].ChunkID != "a" {
		t.Fatalf("заглушка не мала змінювати порядок, отримали топ-1 = %q — схоже, re-ranker уже працює; перепишіть цей тест", got.Candidates[0].ChunkID)
	}
}

// TestBaseline_ConfusesTariffs показує, НАВІЩО потрібен re-ranker: наївний
// keyword overlap ставить чанк про T-1 поруч із чанком про T-2, бо тексти
// майже однакові. Це вихідна точка для таблиці «до/після» у README.
func TestBaseline_ConfusesTariffs(t *testing.T) {
	t.Parallel()
	got, err := newTestRetriever(t).search(newNodeCtx(), "яка ставка комісії на тарифі T-2?")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range got.Candidates {
		ids = append(ids, c.ChunkID)
	}
	if !strings.Contains(strings.Join(ids, " "), "c-A331-rate") {
		t.Skipf("baseline уже не плутає T-1 і T-2 (%v) — оновіть цей приклад під свій пошук", ids)
	}
	t.Logf("baseline top-%d: %v — чанк про T-1 серед кандидатів на запит про T-2", len(ids), ids)
}

// TestRerank_ChangesOrder — КОНТРАКТ ДЗ (15 балів), не заготовка.
//
// Сенс re-ranker-а в тому, що він піднімає справді релевантний чанк над
// семантично близьким, але нерелевантним. Класичний приклад із лекції:
// на запит про T-2 vector повертає нагору чанк про T-1, бо тексти майже
// однакові; cross-encoder бачить пару «запит ↔ чанк» цілком і опускає його.
//
// TODO(студент): приберіть t.Skip і підставте свої кандидати. Пару «до/після»
// із цього тесту можна одразу класти в README — це один із п'яти потрібних
// прикладів.
func TestRerank_ChangesOrder(t *testing.T) {
	t.Skip("зніміть skip, коли реалізуєте re-ranker у retrieval.go")

	in := SearchResult{Query: "яка ставка комісії на тарифі T-2?", Candidates: []Candidate{
		{ChunkID: "c-A331-rate", DocumentID: "ledgerworks_soc2", Text: "Мерчант A-331 обслуговується за тарифом T-1, ставка комісії 1.8 %.", Score: 0.88},
		{ChunkID: "c-A114-rate", DocumentID: "ledgerworks_soc2", Text: "Мерчант A-114 обслуговується за тарифом T-2, ставка комісії 2.9 %.", Score: 0.86},
	}}

	got, err := newTestRetriever(t).rerank(newNodeCtx(), in)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if len(got.Candidates) == 0 {
		t.Fatal("re-ranker не лишив жодного кандидата")
	}
	if got.Candidates[0].ChunkID != "c-A114-rate" {
		t.Errorf("топ-1 = %q, очікували c-A114-rate: re-ranker не підняв чанк про T-2 над чанком про T-1",
			got.Candidates[0].ChunkID)
	}
}

// --- Допоміжне ---------------------------------------------------------------

func TestChunksPath(t *testing.T) {
	t.Parallel()
	if got := chunksPath(""); got != defaultChunks {
		t.Errorf("chunksPath(\"\") = %q", got)
	}
	if got := chunksPath("my.json"); got != "my.json" {
		t.Errorf("chunksPath(my.json) = %q", got)
	}
}
