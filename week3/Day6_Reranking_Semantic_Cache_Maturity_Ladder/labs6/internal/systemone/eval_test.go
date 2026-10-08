package systemone

// Live eval: does the decision model work on this lab's Ukrainian corpus?
//
// Not a unit test. It needs a running model, so every test here skips unless
// SYSTEMONE_URL is set:
//
//	ollama pull tev1:4b
//	SYSTEMONE_URL=http://localhost:11434 go test -run TestEval -v .
//	task week3:day6:eval:systemone           # the same, with the server check
//
// The cases are labelled by hand against testdata/chunks.json. Negatives are
// deliberately hard: the same words about a different tariff, merchant or
// contract — the mistakes the keyword baseline makes. A model that passes
// here can re-rank this corpus and guard its cache. Run it again after
// changing SYSTEMONE_MODEL, the question wording or the corpus.
//
// Measured 08.10.2026, tev1:4b, Ollama 0.40.1, all tests pass:
//
//	rerank   TP=10 FP=0 TN=11 FN=0 | lowest positive 0.777, highest negative 0.148
//	same     TP=5  FP=0 TN=5  FN=0 | lowest positive 0.798, highest negative 0.165
//	out-of-domain best chunk 0.014; multi-hop signer chunk 0.119; deterministic

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/dimetron/ai-eng-course/labs/week3/Day6_Reranking_Semantic_Cache_Maturity_Ladder/labs6/internal/corpus"
)

// evalThreshold is the P(yes) above which an answer counts as "yes". 0.5 is
// the neutral point of a calibrated noul; the eval reports the margin on
// either side so a stricter threshold can be chosen from evidence.
const evalThreshold = 0.5

// liveClient returns the configured client or skips the test.
func liveClient(t *testing.T) *Client {
	t.Helper()
	c := New()
	if c == nil {
		t.Skip("live eval: set SYSTEMONE_URL (e.g. http://localhost:11434) and pull the model")
	}
	return c
}

// evalCtx bounds one live test: a cold model load can take a while, a hung
// server must not hang the suite.
func evalCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// loadChunks reads the lab corpus, keyed by chunk id.
func loadChunks(t *testing.T) map[string]string {
	t.Helper()
	chunks, err := corpus.Load("../../testdata/chunks.json")
	if err != nil {
		t.Fatalf("corpus: %v", err)
	}
	byID := make(map[string]string, len(chunks))
	for _, c := range chunks {
		byID[c.ID] = c.Text
	}
	return byID
}

// rerankCase is one query with the chunk that answers it and hard negatives.
type rerankCase struct {
	query     string
	positive  string   // chunk id that answers the query
	negatives []string // chunk ids that must not
}

var rerankCases = []rerankCase{
	{"Яка ставка комісії на тарифі T-2?", "c-tariff-T2", []string{"c-tariff-T1"}},
	{"Яка ставка комісії на тарифі T-1?", "c-tariff-T1", []string{"c-tariff-T2"}},
	{"Хто підписав договір CT-2025-031?", "c-sign-031", []string{"c-sign-014", "c-A207-rate"}},
	{"Хто підписав договір CT-2025-007?", "c-sign-007", []string{"c-sign-014"}},
	{"За яким тарифом обслуговується мерчант A-331?", "c-A331-rate", []string{"c-A114-rate"}},
	{"Який договір у мерчанта A-114?", "c-A114-rate", []string{"c-A207-rate"}},
	{"На кого поширюється вимога НБУ 2026?", "c-nbu-req", []string{"c-nbu-deadline"}},
	{"Як часто подається звіт про відповідність НБУ 2026?", "c-nbu-deadline", []string{"c-nbu-req"}},
	{"Що не так зі змінами в продуктивному середовищі Acme Bank?", "c-F104", []string{"c-availability"}},
	{"Скільки інцидентів недоступності було в Northwind Cloud?", "c-availability", []string{"c-F104"}},
}

// verdict counts a binary classification at evalThreshold.
type verdict struct {
	tp, fp, tn, fn int
	minPos, maxNeg float64
}

func newVerdict() verdict { return verdict{minPos: math.Inf(1), maxNeg: math.Inf(-1)} }

func (v *verdict) add(label bool, p float64) string {
	yes := p >= evalThreshold
	switch {
	case label && yes:
		v.tp++
	case label:
		v.fn++
	case yes:
		v.fp++
	default:
		v.tn++
	}
	if label {
		v.minPos = math.Min(v.minPos, p)
	} else {
		v.maxNeg = math.Max(v.maxNeg, p)
	}
	if yes == label {
		return "ok"
	}
	if yes {
		return "FP"
	}
	return "FN"
}

func (v verdict) String() string {
	return fmt.Sprintf("TP=%d FP=%d TN=%d FN=%d | lowest positive %.3f, highest negative %.3f",
		v.tp, v.fp, v.tn, v.fn, v.minPos, v.maxNeg)
}

// TestEval_Rerank: every labelled passage is classified right, and for every
// query the answering chunk ranks first. False positives are the costly
// mistake — a wrong chunk reaches the answer with a confident score — so the
// gate is zero of them.
func TestEval_Rerank(t *testing.T) {
	c := liveClient(t)
	ctx := evalCtx(t)
	chunks := loadChunks(t)
	t.Logf("model: %s", c.Name())

	v := newVerdict()
	for _, tc := range rerankCases {
		ids := append([]string{tc.positive}, tc.negatives...)
		texts := make([]string, len(ids))
		for i, id := range ids {
			text, ok := chunks[id]
			if !ok {
				t.Fatalf("chunk %q is not in testdata/chunks.json", id)
			}
			texts[i] = text
		}
		scored, err := c.Rerank(ctx, tc.query, texts)
		if err != nil {
			t.Fatalf("Rerank(%q): %v", tc.query, err)
		}
		for _, s := range scored {
			mark := v.add(s.Index == 0, s.Score)
			t.Logf("%s %.3f  %-50s | %s", mark, s.Score, tc.query, ids[s.Index])
		}
		if scored[0].Index != 0 {
			t.Errorf("%q: top chunk is %s, want %s", tc.query, ids[scored[0].Index], tc.positive)
		}
	}
	t.Logf("rerank: %s", v)
	if v.fp > 0 {
		t.Errorf("rerank: %d false positives at %.2f — a wrong chunk would pass as relevant", v.fp, evalThreshold)
	}
	if v.fn > 0 {
		t.Errorf("rerank: %d answering chunks scored below %.2f", v.fn, evalThreshold)
	}
}

// TestEval_Rerank_MultiHopIsNotOneHop documents a limit, not a defect: the
// signer chunk answers "when was merchant A-207's contract signed?" only via
// A-207 → CT-2025-031 → signer, and a single passage cannot show that hop. The
// model is right to score it low; graph_traversal (Homework item 2) is what
// answers it. If this starts to fail, the model is guessing the hop.
func TestEval_Rerank_MultiHopIsNotOneHop(t *testing.T) {
	c := liveClient(t)
	chunks := loadChunks(t)
	scored, err := c.Rerank(evalCtx(t), "Коли підписали договір мерчанта A-207?", []string{chunks["c-sign-031"]})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	t.Logf("multi-hop signer chunk: %.3f", scored[0].Score)
	if scored[0].Score >= evalThreshold {
		t.Errorf("signer chunk scored %.3f without the A-207 → CT-2025-031 hop in view", scored[0].Score)
	}
}

// TestEval_OutOfDomain: a question the corpus cannot answer scores low against
// every chunk — the evidence a "not enough evidence" terminal node needs.
func TestEval_OutOfDomain(t *testing.T) {
	c := liveClient(t)
	chunks := loadChunks(t)
	ids := []string{"c-tariff-T2", "c-availability", "c-sign-031", "c-nbu-req"}
	texts := make([]string, len(ids))
	for i, id := range ids {
		texts[i] = chunks[id]
	}
	for _, q := range []string{"Яка погода в Києві?", "Хто виграв чемпіонат світу з футболу 2022 року?"} {
		scored, err := c.Rerank(evalCtx(t), q, texts)
		if err != nil {
			t.Fatalf("Rerank(%q): %v", q, err)
		}
		t.Logf("%-50s best %.3f (%s)", q, scored[0].Score, ids[scored[0].Index])
		if scored[0].Score >= evalThreshold {
			t.Errorf("%q: %s scored %.3f, want every chunk below %.2f", q, ids[scored[0].Index], scored[0].Score, evalThreshold)
		}
	}
}

// sameCase is a pair of questions and whether one answer serves both.
type sameCase struct {
	a, b string
	same bool
}

var sameCases = []sameCase{
	{"Що таке тариф T-2?", "Розкажи про тарифний план T-2", true},
	{"Скільки коштує тариф T-2?", "Яка ціна тарифу T-2?", true},
	{"Хто підписав договір CT-2025-031?", "Чий підпис стоїть на договорі CT-2025-031?", true},
	{"Яка комісія для мерчанта A-114?", "Скільки відсотків платить мерчант A-114?", true},
	{"На кого поширюється вимога НБУ 2026?", "Кого стосується вимога НБУ 2026?", true},
	// Hard negatives: one token apart — exactly where cosine similarity fails.
	{"Що таке тариф T-2?", "Що таке тариф T-1?", false},
	{"Хто підписав договір CT-2025-031?", "Хто підписав договір CT-2025-014?", false},
	{"Яка комісія для мерчанта A-114?", "Яка комісія для мерчанта A-331?", false},
	{"Скільки коштує тариф T-2?", "Хто підписав договори на тарифі T-2?", false},
	{"На кого поширюється вимога НБУ 2026?", "Як часто подається звіт НБУ 2026?", false},
}

// TestEval_Same: the semantic-cache check. A false positive here serves one
// customer's answer for another question, so the gate is zero of them and a
// clear margin between the classes.
func TestEval_Same(t *testing.T) {
	c := liveClient(t)
	ctx := evalCtx(t)
	t.Logf("model: %s", c.Name())

	v := newVerdict()
	for _, tc := range sameCases {
		p, err := c.Same(ctx, tc.a, tc.b)
		if err != nil {
			t.Fatalf("Same(%q, %q): %v", tc.a, tc.b, err)
		}
		t.Logf("%s %.3f  %-40s ~ %s", v.add(tc.same, p), p, tc.a, tc.b)
	}
	t.Logf("same: %s", v)
	if v.fp > 0 || v.fn > 0 {
		t.Errorf("same: %d false positives, %d false negatives at %.2f", v.fp, v.fn, evalThreshold)
	}
	if v.minPos <= v.maxNeg {
		t.Errorf("same: classes overlap (lowest positive %.3f ≤ highest negative %.3f) — no threshold separates them", v.minPos, v.maxNeg)
	}
}

// TestEval_Deterministic: one forward pass, no sampling — the same request
// must give the same score, or an eval run cannot be compared with the next.
func TestEval_Deterministic(t *testing.T) {
	c := liveClient(t)
	ctx := evalCtx(t)
	chunks := loadChunks(t)
	q, text := "Яка ставка комісії на тарифі T-2?", chunks["c-tariff-T2"]
	var first float64
	for i := range 3 {
		scored, err := c.Rerank(ctx, q, []string{text})
		if err != nil {
			t.Fatalf("Rerank: %v", err)
		}
		if i == 0 {
			first = scored[0].Score
			continue
		}
		if scored[0].Score != first {
			t.Errorf("run %d scored %v, run 1 scored %v", i+1, scored[0].Score, first)
		}
	}
}
