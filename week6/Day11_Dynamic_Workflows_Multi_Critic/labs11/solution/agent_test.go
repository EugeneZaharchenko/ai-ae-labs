package main

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
)

// ── фікстури ────────────────────────────────────────────────────────────────

const (
	goodCode = "package p\n\nfunc F() bool { return true }\n"
	todoCode = "package p\n\nfunc F() bool {\n\t// TODO\n\treturn false\n}\n"
)

func verdictJSON(r Role, passed bool, comments ...string) string {
	q := make([]string, 0, len(comments))
	for _, c := range comments {
		q = append(q, fmt.Sprintf("%q", c))
	}
	return fmt.Sprintf(`{"critic":%q,"passed":%t,"comments":[%s]}`,
		r, passed, strings.Join(q, ","))
}

// allPass — відповіді панелі, де всі троє задоволені.
func allPass() []RawCritique {
	return []RawCritique{
		{Role: RoleCorrectness, Raw: verdictJSON(RoleCorrectness, true)},
		{Role: RoleStyle, Raw: verdictJSON(RoleStyle, true)},
		{Role: RolePerformance, Raw: verdictJSON(RolePerformance, true)},
	}
}

// ── 1. Табличний тест на машину станів циклу ────────────────────────────────

// TestRunLoop фіксує три сценарії бюджету: успіх з першої, успіх з третьої
// після фідбеку, і чесне вичерпання maxAttempts.
//
// Третій кейс — найважливіший. Цикл, який «майже завжди сходиться», у проді
// одного разу не зійдеться, і тоді єдине, що має значення, — чи він
// зупиняється сам і чи каже, чому.
func TestRunLoop(t *testing.T) {
	tests := []struct {
		name          string
		maxAttempts   int
		codeByAttempt func(n int) string
		critique      func(n int) []RawCritique
		wantAccepted  bool
		wantAttempts  int
		wantCritics   int
		wantErr       error
	}{
		{
			name:          "проходить з першої спроби",
			maxAttempts:   4,
			codeByAttempt: func(int) string { return goodCode },
			critique:      func(int) []RawCritique { return allPass() },
			wantAccepted:  true,
			wantAttempts:  1,
			wantCritics:   1,
		},
		{
			name:        "сходиться на третій спробі після фідбеку грейдера",
			maxAttempts: 4,
			codeByAttempt: func(n int) string {
				if n < 3 {
					return todoCode
				}
				return goodCode
			},
			critique:     func(int) []RawCritique { return allPass() },
			wantAccepted: true,
			wantAttempts: 3,
			// Спроби 1–2 зрізав детермінований грейдер: критики працювали
			// рівно один раз, на третій.
			wantCritics: 1,
		},
		{
			name:          "сходиться на третій спробі після фідбеку критиків",
			maxAttempts:   4,
			codeByAttempt: func(int) string { return goodCode },
			critique: func(n int) []RawCritique {
				if n < 3 {
					return []RawCritique{
						{Role: RoleCorrectness, Raw: verdictJSON(RoleCorrectness, false, "не оброблено порожній рядок")},
						{Role: RoleStyle, Raw: verdictJSON(RoleStyle, true)},
						{Role: RolePerformance, Raw: verdictJSON(RolePerformance, true)},
					}
				}
				return allPass()
			},
			wantAccepted: true,
			wantAttempts: 3,
			wantCritics:  3,
		},
		{
			name:          "вичерпує бюджет і падає явно",
			maxAttempts:   3,
			codeByAttempt: func(int) string { return todoCode },
			critique:      func(int) []RawCritique { return allPass() },
			wantAccepted:  false,
			wantAttempts:  3,
			wantCritics:   0,
			wantErr:       ErrBudgetExhausted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seenFeedback [][]string
			steps := Steps{
				Make: func(_ context.Context, d Draft) (string, error) {
					seenFeedback = append(seenFeedback, d.Feedback)
					return tt.codeByAttempt(d.Attempt), nil
				},
				Critique: func(_ context.Context, d Draft) ([]RawCritique, error) {
					return tt.critique(d.Attempt), nil
				},
			}

			res, err := RunLoop(context.Background(), LoopConfig{
				MaxAttempts: tt.maxAttempts,
				Grader:      NewRuleGrader(),
			}, steps, "напиши F")

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, хотіли %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("несподівана помилка: %v", err)
			}

			if res.Accepted != tt.wantAccepted {
				t.Errorf("Accepted = %t, хотіли %t", res.Accepted, tt.wantAccepted)
			}
			if got := len(res.Attempts); got != tt.wantAttempts {
				t.Errorf("спроб = %d, хотіли %d\n%s", got, tt.wantAttempts, res.Report())
			}
			if got := res.CriticCalls(); got != tt.wantCritics {
				t.Errorf("раундів із критиками = %d, хотіли %d\n%s", got, tt.wantCritics, res.Report())
			}

			// Перша спроба завжди без фідбеку; кожна наступна — з ним.
			// Інакше «цикл виправлень» виправляє наосліп.
			if len(seenFeedback) > 0 && len(seenFeedback[0]) != 0 {
				t.Errorf("спроба 1 отримала фідбек %v, хотіли порожній", seenFeedback[0])
			}
			for i := 1; i < len(seenFeedback); i++ {
				if len(seenFeedback[i]) == 0 {
					t.Errorf("спроба %d пішла без фідбеку — maker не знає, що виправляти", i+1)
				}
			}
		})
	}
}

// TestRunLoop_RejectsZeroBudget — цикл без бюджету не запускається взагалі.
func TestRunLoop_RejectsZeroBudget(t *testing.T) {
	_, err := RunLoop(context.Background(), LoopConfig{Grader: NewRuleGrader()}, Steps{
		Make:     func(context.Context, Draft) (string, error) { return goodCode, nil },
		Critique: func(context.Context, Draft) ([]RawCritique, error) { return allPass(), nil },
	}, "任")
	if err == nil {
		t.Fatal("хотіли помилку на MaxAttempts=0, отримали nil")
	}
}

// TestLoopTimeout — F9: весь multi_critic_loop має власну стелю, більшу за
// суму per-node таймаутів на MaxAttempts спроб, а не 0 (workflow.NodeConfig{}
// без Timeout означає «без обмежень» для всього вузла).
func TestLoopTimeout(t *testing.T) {
	got := loopTimeout(DefaultMaxAttempts)
	perAttempt := makerTimeout + criticTimeout + gateTimeout
	want := time.Duration(DefaultMaxAttempts+1) * perAttempt
	if got != want {
		t.Fatalf("loopTimeout(%d) = %v, хотіли %v", DefaultMaxAttempts, got, want)
	}
	if got <= time.Duration(DefaultMaxAttempts)*perAttempt {
		t.Fatalf("loopTimeout(%d) = %v має бути суворо більше за %d спроб без запасу",
			DefaultMaxAttempts, got, DefaultMaxAttempts)
	}
}

// ── 2. Схема вердикта ───────────────────────────────────────────────────────

// TestParseVerdict перевіряє, що саме відсікається на межі «текст моделі →
// типізований вердикт».
func TestParseVerdict(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		expected Role
		wantErr  bool
	}{
		{
			name:     "валідний вердикт",
			raw:      verdictJSON(RoleStyle, true),
			expected: RoleStyle,
		},
		{
			name:     "валідний вердикт у markdown-фенсі",
			raw:      "```json\n" + verdictJSON(RoleStyle, true) + "\n```",
			expected: RoleStyle,
		},
		{
			// F9: реальний критик часто дописує щось після JSON — це не
			// частина контракту і не має провалювати весь кворум.
			name:     "валідний вердикт із прозою після JSON",
			raw:      verdictJSON(RoleStyle, true) + "\n\nСподіваюсь, це допоможе!",
			expected: RoleStyle,
		},
		{
			name:     "валідний вердикт із прозою перед JSON",
			raw:      "Ось мій вердикт:\n" + verdictJSON(RoleStyle, true),
			expected: RoleStyle,
		},
		{
			name:     "провал із коментарем",
			raw:      verdictJSON(RoleCorrectness, false, "не оброблено nil"),
			expected: RoleCorrectness,
		},
		{
			name:     "проза замість JSON",
			raw:      "так, код виглядає ок",
			expected: RoleStyle,
			wantErr:  true,
		},
		{
			name:     "порожня відповідь",
			raw:      "   ",
			expected: RoleStyle,
			wantErr:  true,
		},
		{
			name:     "невідома роль",
			raw:      `{"critic":"magic","passed":true}`,
			expected: RoleStyle,
			wantErr:  true,
		},
		{
			// Критик стилю видає себе за блокуючу роль — саме те, чого
			// домагається prompt injection у коді-кандидаті.
			name:     "підміна ролі",
			raw:      verdictJSON(RoleCorrectness, true),
			expected: RoleStyle,
			wantErr:  true,
		},
		{
			name:     "провал без коментарів",
			raw:      verdictJSON(RolePerformance, false),
			expected: RolePerformance,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := ParseVerdict(tt.raw, tt.expected)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("хотіли помилку, отримали вердикт %+v", v)
				}
				if !errors.Is(err, ErrInvalidVerdict) {
					t.Errorf("err = %v, хотіли обгортку ErrInvalidVerdict", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("несподівана помилка: %v", err)
			}
			if v.Critic != tt.expected {
				t.Errorf("Critic = %q, хотіли %q", v.Critic, tt.expected)
			}
		})
	}
}

// TestRunLoop_InvalidVerdictDoesNotFlowForward — головний негативний тест
// частини.
//
// Блокуючий критик повертає сміття. Правильна поведінка — НЕ «якось
// інтерпретувати» і НЕ «вважати, що мовчання = згода», а зафіксувати
// відсутність кворуму і відхилити кандидата, який у всьому іншому ідеальний.
func TestRunLoop_InvalidVerdictDoesNotFlowForward(t *testing.T) {
	steps := Steps{
		Make: func(context.Context, Draft) (string, error) { return goodCode, nil },
		Critique: func(context.Context, Draft) ([]RawCritique, error) {
			return []RawCritique{
				{Role: RoleCorrectness, Raw: "ага, все ок"}, // сміття
				{Role: RoleStyle, Raw: verdictJSON(RoleStyle, true)},
				{Role: RolePerformance, Raw: verdictJSON(RolePerformance, true)},
			}, nil
		},
	}

	res, err := RunLoop(context.Background(), LoopConfig{
		MaxAttempts: 2,
		Grader:      NewRuleGrader(),
	}, steps, "напиши F")

	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("err = %v, хотіли ErrBudgetExhausted", err)
	}
	if res.Accepted {
		t.Fatal("кандидата прийнято попри зіпсований вердикт блокуючого критика")
	}
	last := res.Attempts[len(res.Attempts)-1]
	if len(last.Verdicts) != 2 {
		t.Errorf("валідних вердиктів = %d, хотіли 2 (сміття не мало пройти далі)", len(last.Verdicts))
	}
	if len(last.Rejected) != 1 {
		t.Errorf("відхилених вердиктів = %d, хотіли 1", len(last.Rejected))
	}
	if !strings.Contains(last.Decision.Reason, "кворум") {
		t.Errorf("Reason = %q, хотіли згадку кворуму", last.Decision.Reason)
	}
}

// ── 3. Політика злиття і tie-break ──────────────────────────────────────────

// TestMerge фіксує документовану політику: кворум → блокер → більшість →
// tie-break fail-closed.
func TestMerge(t *testing.T) {
	tests := []struct {
		name       string
		verdicts   []Verdict
		rejected   []string
		wantAccept bool
		wantReason string
	}{
		{
			name: "усі за — приймаємо",
			verdicts: []Verdict{
				{Critic: RoleCorrectness, Passed: true},
				{Critic: RoleStyle, Passed: true},
				{Critic: RolePerformance, Passed: true},
			},
			wantAccept: true,
			wantReason: "прийнято",
		},
		{
			name: "блокер проти — відхиляємо попри одностайних дорадчих",
			verdicts: []Verdict{
				{Critic: RoleCorrectness, Passed: false, Comments: []string{"nil не оброблено"}},
				{Critic: RoleStyle, Passed: true},
				{Critic: RolePerformance, Passed: true},
			},
			wantReason: "блокуюча роль",
		},
		{
			// Документований tie-break: 1 проти 1 серед дорадчих — це не
			// доказ якості, це відсутність доказу. Fail-closed.
			name: "дорадчі розділилися 1:1 — tie-break відхиляє",
			verdicts: []Verdict{
				{Critic: RoleCorrectness, Passed: true},
				{Critic: RoleStyle, Passed: false, Comments: []string{"назва змінної r нічого не каже"}},
				{Critic: RolePerformance, Passed: true},
			},
			wantReason: "tie-break",
		},
		{
			name: "меншість проти — більшість вирішує на користь коду",
			verdicts: []Verdict{
				{Critic: RoleCorrectness, Passed: true},
				{Critic: RoleStyle, Passed: true},
				{Critic: RoleStyle, Passed: true},
				{Critic: RolePerformance, Passed: false, Comments: []string{"зайва алокація"}},
			},
			wantAccept: true,
			wantReason: "прийнято",
		},
		{
			name: "більшість дорадчих проти",
			verdicts: []Verdict{
				{Critic: RoleCorrectness, Passed: true},
				{Critic: RoleStyle, Passed: false, Comments: []string{"а"}},
				{Critic: RolePerformance, Passed: false, Comments: []string{"б"}},
			},
			wantReason: "більшість дорадчих проти",
		},
		{
			name: "немає блокера — немає кворуму",
			verdicts: []Verdict{
				{Critic: RoleStyle, Passed: true},
				{Critic: RolePerformance, Passed: true},
			},
			rejected:   []string{"correctness → сміття"},
			wantReason: "немає кворуму",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Merge(tt.verdicts, tt.rejected)
			if d.Accept != tt.wantAccept {
				t.Errorf("Accept = %t, хотіли %t (reason: %s)", d.Accept, tt.wantAccept, d.Reason)
			}
			if !strings.Contains(d.Reason, tt.wantReason) {
				t.Errorf("Reason = %q, хотіли підрядок %q", d.Reason, tt.wantReason)
			}
			if !d.Accept && len(d.Dissent) == 0 && len(tt.rejected) == 0 {
				t.Error("відхилення без жодного Dissent — оператор не дізнається, хто був проти")
			}
		})
	}
}

// TestRuleGrader — детермінований грейдер: той самий вхід, та сама відповідь.
func TestRuleGrader(t *testing.T) {
	g := NewRuleGrader()

	if rep := g.Grade(context.Background(), goodCode); !rep.OK {
		t.Errorf("валідний код провалено: %+v", rep)
	}

	rep := g.Grade(context.Background(), todoCode)
	if rep.OK {
		t.Fatal("заглушка з TODO пройшла грейдер")
	}
	if rep.Stage != "no_todo" {
		t.Errorf("Stage = %q, хотіли no_todo", rep.Stage)
	}

	// Відсутність дисперсії — це і є причина ставити його перед моделлю.
	for i := range 5 {
		if again := g.Grade(context.Background(), todoCode); again.Stage != rep.Stage || again.OK != rep.OK {
			t.Fatalf("прогін %d дав інший результат: %+v проти %+v", i, again, rep)
		}
	}
}

// ── 4. ADK-шар: порядок «детермінований → модельний» ────────────────────────

// recordingGrader запам'ятовує, скільки викликів до критиків уже сталося на
// момент кожного свого запуску.
type recordingGrader struct {
	inner       Grader
	criticCalls func() int

	mu   sync.Mutex
	seen []int
}

func (g *recordingGrader) Grade(ctx context.Context, code string) GradeReport {
	g.mu.Lock()
	g.seen = append(g.seen, g.criticCalls())
	g.mu.Unlock()
	return g.inner.Grade(ctx, code)
}

func (g *recordingGrader) snapshot() []int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]int(nil), g.seen...)
}

// TestAgent_DeterministicGradeRunsBeforeCritics — навчальний тест частини.
//
// Maker двічі поспіль віддає заглушку з TODO. Детермінований грейдер ріже її
// обидва рази, і жоден критик не отримує жодного виклику: CallCount() усіх
// трьох моделей лишається нулем. Це і є економія, заради якої порядок кроків
// фіксований — платити за думку про стиль коду, якого не існує, безглуздо.
func TestAgent_DeterministicGradeRunsBeforeCritics(t *testing.T) {
	maker := fakellm.New("maker", fakellm.TextTurn(todoCode), fakellm.TextTurn(todoCode))
	correctness := fakellm.New("c1", fakellm.TextTurn(verdictJSON(RoleCorrectness, true)))
	style := fakellm.New("c2", fakellm.TextTurn(verdictJSON(RoleStyle, true)))
	perf := fakellm.New("c3", fakellm.TextTurn(verdictJSON(RolePerformance, true)))

	criticCalls := func() int { return correctness.CallCount() + style.CallCount() + perf.CallCount() }
	grader := &recordingGrader{inner: NewRuleGrader(), criticCalls: criticCalls}

	a, err := NewAgent(Config{
		Maker:       maker,
		Critics:     DefaultCritics(correctness, style, perf),
		Grader:      grader,
		MaxAttempts: 2,
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	if _, err := labrun.Run(context.Background(), a, "напиши F"); err == nil {
		t.Fatal("хотіли помилку вичерпаного бюджету, отримали успіх")
	}

	if got := criticCalls(); got != 0 {
		t.Errorf("викликів до критиків = %d, хотіли 0: детермінований провал має зупиняти раунд", got)
	}
	if got := maker.CallCount(); got != 2 {
		t.Errorf("викликів до maker = %d, хотіли 2 (бюджет)", got)
	}
	seen := grader.snapshot()
	if len(seen) != 2 {
		t.Fatalf("грейдер відпрацював %d разів, хотіли 2", len(seen))
	}
	for i, n := range seen {
		if n != 0 {
			t.Errorf("на момент прогону грейдера #%d уже було %d викликів критиків — порядок порушено", i+1, n)
		}
	}
}

// ── 5. Паралельна панель і злиття через JoinNode ────────────────────────────

// barrier відпускає всіх, хто до нього дійшов, лише коли їх набралося n.
// Якщо критики підуть послідовно, перший же застрягне і тест впаде за
// таймаутом із зрозумілим повідомленням, а не зависне назавжди.
type barrier struct {
	n     int
	mu    sync.Mutex
	count int
	ready chan struct{}
	once  sync.Once
}

func newBarrier(n int) *barrier { return &barrier{n: n, ready: make(chan struct{})} }

func (b *barrier) arrive(timeout time.Duration) error {
	b.mu.Lock()
	b.count++
	reached := b.count >= b.n
	b.mu.Unlock()
	if reached {
		b.once.Do(func() { close(b.ready) })
	}
	select {
	case <-b.ready:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("барʼєр: дочекались %d з %d критиків — панель не паралельна", b.count, b.n)
	}
}

// barrierModel — model.LLM, що спершу відмічається на барʼєрі, а тоді віддає
// скриптовану відповідь.
type barrierModel struct {
	inner *fakellm.Model
	gate  *barrier
}

func (m *barrierModel) Name() string { return m.inner.Name() }

func (m *barrierModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if err := m.gate.arrive(5 * time.Second); err != nil {
		return func(yield func(*model.LLMResponse, error) bool) { yield(nil, err) }
	}
	return m.inner.GenerateContent(ctx, req, stream)
}

// TestAgent_CriticsRunInParallelAndAllVerdictsMerge — fan-out/fan-in.
//
// Три критики стартують одночасно (це доводить барʼєр) і зливаються в один
// map[string]any через JoinNode. Перевіряємо, що в рішення потрапили всі три
// вердикти, а не два з трьох: JoinNode — це барʼєр, він активується один раз
// і лише після всіх попередників.
//
// Запускати під -race: три горутини ходять в одну модель і в один event
// stream.
func TestAgent_CriticsRunInParallelAndAllVerdictsMerge(t *testing.T) {
	gate := newBarrier(3)
	critic := func(name string, r Role) *barrierModel {
		return &barrierModel{
			inner: fakellm.New(name, fakellm.TextTurn(verdictJSON(r, true))),
			gate:  gate,
		}
	}

	maker := fakellm.New("maker", fakellm.TextTurn(goodCode))
	a, err := NewAgent(Config{
		Maker: maker,
		Critics: DefaultCritics(
			critic("c1", RoleCorrectness),
			critic("c2", RoleStyle),
			critic("c3", RolePerformance),
		),
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	res, err := labrun.Run(context.Background(), a, "напиши F")
	if err != nil {
		t.Fatalf("прогін: %v", err)
	}
	if !strings.Contains(res.Final, "func F") {
		t.Errorf("Final = %q, хотіли прийнятий код", res.Final)
	}

	attempt := attemptState(t, res, 1)
	if got := attempt["verdicts"]; got != 3 {
		t.Errorf("злитих вердиктів = %v, хотіли 3", got)
	}
	if got := attempt["rejected"]; got != 0 {
		t.Errorf("відхилених вердиктів = %v, хотіли 0", got)
	}
	if got := attempt["accepted"]; got != true {
		t.Errorf("accepted = %v, хотіли true (reason: %v)", got, attempt["reason"])
	}
}

// ── 6. End-to-end через labrun + fakellm ────────────────────────────────────

// TestAgent_EndToEnd — повний шлях: грейдер ріже першу чернетку, друга
// проходить панель, агент віддає код і лишає слід у session state.
func TestAgent_EndToEnd(t *testing.T) {
	maker := fakellm.New("maker", fakellm.TextTurn(todoCode), fakellm.TextTurn(goodCode))
	correctness := fakellm.New("c1", fakellm.TextTurn(verdictJSON(RoleCorrectness, true)))
	style := fakellm.New("c2", fakellm.TextTurn(verdictJSON(RoleStyle, true)))
	perf := fakellm.New("c3", fakellm.TextTurn(verdictJSON(RolePerformance, true)))

	a, err := NewAgent(Config{
		Maker:       maker,
		Critics:     DefaultCritics(correctness, style, perf),
		MaxAttempts: 4,
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	res, err := labrun.Run(context.Background(), a, "напиши F")
	if err != nil {
		t.Fatalf("прогін: %v", err)
	}
	if !strings.Contains(res.Final, "func F") {
		t.Errorf("Final = %q, хотіли код кандидата", res.Final)
	}

	first := attemptState(t, res, 1)
	if first["critics_run"] != false || first["grader_ok"] != false {
		t.Errorf("спроба 1 = %v, хотіли провал грейдера без критиків", first)
	}
	second := attemptState(t, res, 2)
	if second["accepted"] != true {
		t.Errorf("спроба 2 = %v, хотіли прийняття", second)
	}

	// Кожен критик відпрацював рівно один раз — на спробі 2. Перша спроба
	// їх не торкнулась, і це видно з рахунку, а не лише зі слів.
	for name, m := range map[string]*fakellm.Model{"correctness": correctness, "style": style, "perf": perf} {
		if got := m.CallCount(); got != 1 {
			t.Errorf("критик %s: викликів = %d, хотіли 1", name, got)
		}
	}
	if maker.Remaining() != 0 {
		t.Errorf("невикористаних ходів maker = %d, хотіли 0", maker.Remaining())
	}
}

// TestAgent_TieBreakBlocksAcceptance — та сама політика tie-break, але вже
// через живий граф: стиль проти, продуктивність за, блокер задоволений.
func TestAgent_TieBreakBlocksAcceptance(t *testing.T) {
	maker := fakellm.New("maker", fakellm.TextTurn(goodCode))
	correctness := fakellm.New("c1", fakellm.TextTurn(verdictJSON(RoleCorrectness, true)))
	style := fakellm.New("c2", fakellm.TextTurn(verdictJSON(RoleStyle, false, "перейменуй F на щось осмислене")))
	perf := fakellm.New("c3", fakellm.TextTurn(verdictJSON(RolePerformance, true)))

	a, err := NewAgent(Config{
		Maker:       maker,
		Critics:     DefaultCritics(correctness, style, perf),
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	res, err := labrun.Run(context.Background(), a, "напиши F")
	if err == nil {
		t.Fatal("хотіли вичерпання бюджету, отримали успіх")
	}
	if !strings.Contains(err.Error(), ErrBudgetExhausted.Error()) {
		t.Errorf("err = %v, хотіли згадку вичерпаного бюджету", err)
	}

	attempt := attemptState(t, res, 1)
	reason, _ := attempt["reason"].(string)
	if !strings.Contains(reason, "tie-break") {
		t.Errorf("reason = %q, хотіли документований tie-break", reason)
	}
}

// attemptState дістає запис спроби n зі StateDelta подій прогону.
//
// Слід шукається саме в StateDelta, а не в тексті: event-sourced стан — це
// той самий механізм, яким ADK зберігає сесію, тож тест читає рівно те, що
// побачить оператор у session record.
func attemptState(t *testing.T, res labrun.Result, n int) map[string]any {
	t.Helper()
	key := fmt.Sprintf("w6p1:attempt:%d", n)
	for _, ev := range res.Events {
		if v, ok := ev.Actions.StateDelta[key]; ok {
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("%s має тип %T, хотіли map[string]any", key, v)
			}
			return m
		}
	}
	t.Fatalf("у сліді немає %s", key)
	return nil
}
