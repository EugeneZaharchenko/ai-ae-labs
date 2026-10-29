// Ядро циклу самополіпшення: бюджет (принцип 2), єдина метрика й компаратор
// keep/discard (принцип 3), і сам цикл propose → apply → evaluate → keep-or-revert.
//
// Тут немає жодного імпорту ADK. Це навмисно: цикл самополіпшення — це
// **loop engineering** (control system), а не властивість фреймворку. Видаліть
// agent.go — і Engine лишиться робочим Go-типом із тестами; підключіть замість
// LLM-агента будь-який інший Proposer — і цикл не помітить різниці.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Proposal — те, що агент просить змінити за одну ітерацію.
//
// Path приходить від агента навмисно: якби цикл підставляв шлях сам, гальмо №1
// ніколи б не спрацювало і ми б тестували власну добропорядність, а не захист.
type Proposal struct {
	Path    string
	Content string
	Summary string
	// Tokens — скільки токенів коштувало вигадати пропозицію (UsageMetadata
	// моделі-пропонента). Повертається навіть разом із помилкою: невдала
	// спроба теж коштує грошей.
	Tokens int
}

// Proposer генерує кандидата на нову стратегію.
//
// ADK-агент — лише одна з реалізацій (agent.go). Для тестів цього достатньо
// звичайного зрізу заготовлених пропозицій.
type Proposer interface {
	Propose(ctx context.Context, current StrategyDoc, history []Entry) (Proposal, error)
}

// Measurement — результат одного вимірювання стратегії.
type Measurement struct {
	// Score — ЄДИНА метрика, більше = краще.
	Score float64
	// Tokens — вартість вимірювання за UsageMetadata (0, якщо модель не
	// повідомляє використання).
	Tokens int
	// Detail — людиночитний розбір: які кейси просіли. Це побічний вихід, а
	// не друга метрика: рішення keep/discard його ніколи не читає.
	Detail string
}

// Scorer — «фіксований дешевий цикл оцінки»: перетворює стратегію на ОДНЕ
// число, більше = краще.
//
// Це той самий verifiability-constraint із лекції, виражений у типі: якщо вашу
// властивість не можна звести до float64, порівнюваного між ранами, — патерн до
// неї не застосовується, і жоден eval-tooling цього не виправить.
//
// Разом із помилкою Scorer має повернути й Measurement.Tokens уже витрачене:
// перерваний вимір теж коштував грошей.
type Scorer interface {
	Score(ctx context.Context, candidate StrategyDoc) (Measurement, error)
}

// Guard — детермінована перевірка кандидата ДО оцінки. Повернена помилка
// означає refused. Так у цикл додається те, що не належить ні поверхні, ні
// allowlist: наприклад, заборона вставляти в стратегію відповіді з eval-набору.
type Guard func(StrategyDoc) error

// Budget — принцип 2: фіксований дешевий цикл оцінки.
type Budget struct {
	// MaxExperiments — жорстка стеля кількості ітерацій.
	MaxExperiments int
	// PerExperiment — бюджет часу на одну ітерацію.
	PerExperiment time.Duration
	// Total — сумарний бюджет часу; 0 означає «обмежені лише кількістю».
	Total time.Duration
	// MaxTokens — сумарна стеля токенів (вимір + пропонент, разом із базовим
	// виміром); 0 означає «токени не обмежені». Це жорстка межа з проєкцією:
	// наступний експеримент не стартує, якщо попередній коштував стільки, що
	// ще один вийде за стелю.
	MaxTokens int
}

// Validate відхиляє конфігурацію, у якій бюджет нічого не обмежує.
//
// MaxExperiments = 0 у нашому API — це помилка, а не «нескінченно». Пряма
// аналогія з loopagent.MaxIterations, де 0 означає саме «вічно»: одне й те саме
// нульове значення з протилежним змістом — класичне джерело нічного прогону,
// якого ніхто не замовляв.
func (b Budget) Validate() error {
	if b.MaxExperiments < 1 {
		return errors.New("budget: MaxExperiments має бути ≥ 1 (0 — не «безмежно», а помилка)")
	}
	if b.PerExperiment <= 0 {
		return errors.New("budget: PerExperiment має бути > 0")
	}
	if b.Total < 0 {
		return errors.New("budget: Total не може бути відʼємним")
	}
	if b.MaxTokens < 0 {
		return errors.New("budget: MaxTokens не може бути відʼємним")
	}
	return nil
}

// StopReason — чому цикл зупинився. Порожній рядок означає «ще працюємо».
type StopReason string

const (
	// StopMaxExperiments — вичерпано ліміт ітерацій.
	StopMaxExperiments StopReason = "max_experiments_reached"
	// StopTimeBudget — на наступний повний експеримент не лишилося часу.
	StopTimeBudget StopReason = "time_budget_exhausted"
	// StopProposerFailed — генератор пропозицій зупинився (напр. модель
	// вичерпалась або впала). Це теж зупинка з причиною, а не паніка.
	StopProposerFailed StopReason = "proposer_failed"
	// StopTokenBudget — на наступний експеримент не вистачає токенів.
	StopTokenBudget StopReason = "token_budget_exhausted"
	// StopStrategyMaxAttempts — вичерпано max_attempts, оголошений У САМІЙ
	// стратегії (секція `## Stop conditions` у strategy.md), а не зовнішній
	// Budget.MaxExperiments. Це окрема стеля: Budget — це те, що дозволяє
	// оператор ЗОВНІ, а MaxAttempts — те, що стратегія оголошує про себе.
	// Стратегія, що keep'ається, може змінити цю межу для наступних спроб —
	// саме тому перевірка читає e.doc.MaxAttempts на кожному кроці, а не
	// один раз при Start.
	StopStrategyMaxAttempts StopReason = "strategy_max_attempts_reached"
)

// Accountant рахує витрачений бюджет.
type Accountant struct {
	budget    Budget
	used      int
	spent     time.Duration
	tokens    int
	projected int
}

// NewAccountant створює лічильник бюджету.
func NewAccountant(b Budget) *Accountant { return &Accountant{budget: b} }

// Begin резервує наступний експеримент. Повертає причину зупинки, якщо бюджету
// більше немає.
//
// Часову перевірку зроблено «на повний експеримент уперед»: якщо на ітерацію не
// лишилося цілого PerExperiment, ми її не починаємо. Інакше останній
// експеримент обірветься на середині й дасть метрику, не порівнювану з рештою,
// — а порівнюваність ітерацій і є весь сенс фіксованого бюджету.
func (a *Accountant) Begin() (StopReason, bool) {
	if why := a.Peek(); why != "" {
		return why, false
	}
	a.used++
	return "", true
}

// Peek каже, чи Begin відмовить зараз, нічого не резервуючи.
func (a *Accountant) Peek() StopReason {
	if a.used >= a.budget.MaxExperiments {
		return StopMaxExperiments
	}
	if a.budget.Total > 0 && a.spent+a.budget.PerExperiment > a.budget.Total {
		return StopTimeBudget
	}
	if a.budget.MaxTokens > 0 && a.tokens+a.projected > a.budget.MaxTokens {
		return StopTokenBudget
	}
	return ""
}

// ChargeTokens списує токени. Якщо exp — це повний експеримент, його вартість
// стає проєкцією вартості наступного.
func (a *Accountant) ChargeTokens(n int, exp bool) {
	if n <= 0 {
		return
	}
	a.tokens += n
	if exp {
		a.projected = n
	}
}

// Project задає початкову проєкцію вартості експерименту (базовий вимір).
func (a *Accountant) Project(n int) {
	if n > 0 {
		a.projected = n
	}
}

// Tokens повертає витрачені токени.
func (a *Accountant) Tokens() int { return a.tokens }

// Charge списує витрачений час.
func (a *Accountant) Charge(d time.Duration) {
	if d > 0 {
		a.spent += d
	}
}

// Used повертає кількість розпочатих експериментів.
func (a *Accountant) Used() int { return a.used }

// Spent повертає витрачений час.
func (a *Accountant) Spent() time.Duration { return a.spent }

// Decide — компаратор фітнесу (принцип 3).
//
// Політика нічиєї задокументована явно й навмисно сувора: **нічия — це
// discard**. Різниця в межах epsilon не є доказом покращення, а стратегія, яку
// приймають «бо не гірше», за десяток ітерацій дрейфує випадковим блуканням і
// робить фінальний артефакт невідтворюваним. Keep вимагає доказу, а не
// відсутності заперечень.
func Decide(prev, candidate, epsilon float64) (Verdict, string) {
	delta := candidate - prev
	switch {
	case delta > epsilon:
		return VerdictKeep, fmt.Sprintf("метрика зросла %.4f → %.4f (+%.4f > ε=%.4f)",
			prev, candidate, delta, epsilon)
	case delta < -epsilon:
		return VerdictDiscard, fmt.Sprintf("регресія %.4f → %.4f (%.4f)", prev, candidate, delta)
	default:
		return VerdictDiscard, fmt.Sprintf("нічия %.4f → %.4f (|%.4f| ≤ ε=%.4f); "+
			"без доказу покращення зміну відкочено", prev, candidate, delta, epsilon)
	}
}

// Engine — control system циклу самополіпшення.
//
// Не є безпечним для конкурентного використання: цикл послідовний за
// визначенням, бо кожна ітерація порівнюється з попередньою.
type Engine struct {
	// Surface — єдиний мутабельний артефакт (гальмо 1).
	Surface *Surface
	// Allowlist — дозволені критики (гальмо 2), перевіряється парсером стратегії.
	Allowlist []string
	// Budget — фіксований бюджет (гальмо 3).
	Budget Budget
	// Epsilon — поріг, нижче якого різниця метрики вважається шумом.
	Epsilon float64
	// Guards — додаткові детерміновані перевірки кандидата до оцінки.
	Guards []Guard
	// Proposer генерує кандидатів, Scorer їх оцінює, Audit усе фіксує.
	Proposer Proposer
	Scorer   Scorer
	Audit    *AuditLog
	// Now дає змогу підставити детермінований годинник у тестах.
	Now func() time.Time

	acct    *Accountant
	doc     StrategyDoc
	current float64
	detail  string
	history []Entry
	started bool

	mu sync.Mutex // захищає лише Brief: його читає горутина пропонента
}

// Report — підсумок прогону.
type Report struct {
	Baseline    float64
	Best        float64
	Tokens      int
	Experiments int
	Keeps       int
	Discards    int
	Refusals    int
	Stop        StopReason
	StopDetail  string
	Entries     []Entry
}

// Start валідує конфігурацію, читає базову стратегію й вимірює базову метрику.
// Ідемпотентний: ADK-шар викликає його ліниво на першій ітерації.
//
// Базовий вимір навмисно не списується з бюджету: це не експеримент, а точка
// відліку, без якої перший keep не має з чим порівнюватись.
func (e *Engine) Start(ctx context.Context) error {
	if e.started {
		return nil
	}
	switch {
	case e.Surface == nil:
		return errors.New("engine: не оголошено поверхню змін")
	case e.Proposer == nil:
		return errors.New("engine: не задано Proposer")
	case e.Scorer == nil:
		return errors.New("engine: не задано Scorer")
	case e.Audit == nil:
		return errors.New("engine: не задано Audit; цикл без аудиту заборонений")
	case e.Epsilon < 0:
		return errors.New("engine: Epsilon не може бути відʼємним")
	}
	if err := e.Budget.Validate(); err != nil {
		return err
	}
	if e.Now == nil {
		e.Now = time.Now
	}

	raw, err := e.Surface.Read()
	if err != nil {
		return fmt.Errorf("engine: читання стратегії %s: %w", e.Surface.Rel(), err)
	}
	doc, err := ParseStrategy(string(raw), e.Allowlist)
	if err != nil {
		// Зіпсована БАЗОВА стратегія — фатальна помилка, а не перша ітерація.
		// Якщо стартовий control point недійсний, порівнювати нема з чим.
		return fmt.Errorf("engine: базова стратегія недійсна: %w", err)
	}
	m, err := e.Scorer.Score(ctx, doc)
	e.acct = NewAccountant(e.Budget)
	e.acct.ChargeTokens(m.Tokens, false)
	if err != nil {
		return fmt.Errorf("engine: базовий вимір метрики: %w", err)
	}
	e.acct.Project(m.Tokens)

	e.mu.Lock()
	e.doc, e.current, e.detail = doc, m.Score, m.Detail
	e.mu.Unlock()
	e.started = true
	return nil
}

// Step виконує рівно одну ітерацію.
//
// Повертає (запис, "", nil) для виконаного експерименту або (нульовий запис,
// причина, nil), коли бюджет вичерпано. Помилка повертається лише тоді, коли
// зламано сам механізм (не вдалося записати аудит чи відкотити файл) — тобто
// коли продовжувати означало б втратити слід.
func (e *Engine) Step(ctx context.Context) (Entry, StopReason, error) {
	if !e.started {
		return Entry{}, "", errors.New("engine: викличте Start перед Step")
	}
	if e.doc.MaxAttempts > 0 && e.acct.Used() >= e.doc.MaxAttempts {
		return Entry{}, StopStrategyMaxAttempts, nil
	}
	if reason, ok := e.acct.Begin(); !ok {
		return Entry{}, reason, nil
	}

	expCtx, cancel := context.WithTimeout(ctx, e.Budget.PerExperiment)
	defer cancel()

	start := e.Now()
	entry := Entry{
		Attempt:   e.acct.Used(),
		TS:        start,
		PrevScore: e.current,
	}

	prop, err := e.Proposer.Propose(expCtx, e.doc, e.history)
	entry.ProposerTokens = prop.Tokens
	if err != nil {
		// Зупинка циклу все одно мусить лишити слід: без цього запису
		// experiments.log просто закінчується на N-1 записі, і немає способу
		// відрізнити «бюджет вичерпано» від «модель впала», перечитуючи лише
		// файл. finish() списує час сам, тож тут його не рахуємо вдруге.
		entry.Verdict = VerdictProposerFailed
		entry.Reason = err.Error()
		if _, _, ferr := e.finish(entry, start); ferr != nil {
			return Entry{}, "", ferr
		}
		return Entry{}, StopProposerFailed, nil
	}
	entry.TargetPath = prop.Path
	entry.DiffSummary = prop.Summary

	// Гальмо 1 — поверхня змін. Перевіряємо ТУТ, навіть якщо інструмент
	// ADK-шару вже перевірив те саме: цикл не зобовʼязаний довіряти своєму
	// викликачеві, і саме дублювання робить гальма незалежними.
	if _, err := e.Surface.Resolve(prop.Path); err != nil {
		entry.Verdict, entry.Reason = VerdictRefused, err.Error()
		return e.finish(entry, start)
	}

	// Гальма 2 і 3 (allowlist критиків, рівно одна метрика) живуть у парсері.
	candidate, err := ParseStrategy(prop.Content, e.Allowlist)
	if err != nil {
		entry.Verdict, entry.Reason = VerdictRefused, err.Error()
		return e.finish(entry, start)
	}

	for _, g := range e.Guards {
		if err := g(candidate); err != nil {
			entry.Verdict, entry.Reason = VerdictRefused, err.Error()
			return e.finish(entry, start)
		}
	}

	previous, err := e.Surface.Read()
	if err != nil {
		return Entry{}, "", fmt.Errorf("engine: знімок стратегії перед зміною: %w", err)
	}
	if err := e.Surface.WritePath(prop.Path, []byte(prop.Content)); err != nil {
		return Entry{}, "", fmt.Errorf("engine: застосування кандидата: %w", err)
	}

	m, scoreErr := e.Scorer.Score(expCtx, candidate)
	entry.EvalTokens = m.Tokens
	if scoreErr != nil {
		// Оцінка не відбулася — кандидат не має доказу, отже відкат. Невдалий
		// вимір не має ставати мовчазним keep.
		if err := e.Surface.Write(previous); err != nil {
			return Entry{}, "", fmt.Errorf("engine: відкат після невдалої оцінки: %w", err)
		}
		entry.Verdict = VerdictDiscard
		entry.Reason = fmt.Sprintf("оцінку не завершено: %v", scoreErr)
		return e.finish(entry, start)
	}

	entry.Scored, entry.NewScore, entry.Detail = true, m.Score, m.Detail
	entry.Verdict, entry.Reason = Decide(e.current, m.Score, e.Epsilon)
	if entry.Verdict == VerdictKeep {
		e.mu.Lock()
		e.current, e.doc, e.detail = m.Score, candidate, m.Detail
		e.mu.Unlock()
	} else if err := e.Surface.Write(previous); err != nil {
		return Entry{}, "", fmt.Errorf("engine: відкат після discard: %w", err)
	}
	return e.finish(entry, start)
}

// finish списує час, доповнює запис станом артефакту й дописує його в аудит.
// Через цю функцію проходить КОЖНА ітерація — і keep, і discard, і refused, —
// тому «непомічених» експериментів у логу не буває.
func (e *Engine) finish(entry Entry, start time.Time) (Entry, StopReason, error) {
	elapsed := e.Now().Sub(start)
	e.acct.Charge(elapsed)
	e.acct.ChargeTokens(entry.EvalTokens+entry.ProposerTokens, true)
	entry.DurationMS = elapsed.Milliseconds()
	if !entry.Scored {
		entry.NewScore = entry.PrevScore
	}
	if cur, err := e.Surface.Read(); err == nil {
		entry.StrategySHA = contentSHA(cur)
	}

	saved, err := e.Audit.Append(entry)
	if err != nil {
		return Entry{}, "", err
	}
	e.history = append(e.history, saved)
	return saved, "", nil
}

// Run проганяє цикл до вичерпання бюджету й повертає підсумок.
func (e *Engine) Run(ctx context.Context) (Report, error) {
	if err := e.Start(ctx); err != nil {
		return Report{}, err
	}
	rep := Report{Baseline: e.current}
	for {
		entry, reason, err := e.Step(ctx)
		if err != nil {
			return rep, err
		}
		if reason != "" {
			rep.Stop = reason
			if reason == StopProposerFailed {
				rep.StopDetail = "генератор пропозицій зупинився раніше за бюджет"
			}
			break
		}
		rep.Entries = append(rep.Entries, entry)
		switch entry.Verdict {
		case VerdictKeep:
			rep.Keeps++
		case VerdictDiscard:
			rep.Discards++
		case VerdictRefused:
			rep.Refusals++
		}
	}
	rep.Experiments = len(rep.Entries)
	rep.Best = e.current
	rep.Tokens = e.acct.Tokens()
	return rep, nil
}

// Strategy повертає стратегію, яка наразі є базовою (після останнього keep).
func (e *Engine) Strategy() StrategyDoc {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.doc
}

// CurrentScore повертає метрику базового стану.
func (e *Engine) CurrentScore() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.current
}

// Exhausted повідомляє, чи наступний Step одразу зупиниться, — не змінюючи
// стану. Порожній рядок означає «ще можна».
func (e *Engine) Exhausted() StopReason {
	if !e.started {
		return ""
	}
	if e.doc.MaxAttempts > 0 && e.acct.Used() >= e.doc.MaxAttempts {
		return StopStrategyMaxAttempts
	}
	return e.acct.Peek()
}

// Tokens повертає токени, витрачені циклом на цей момент.
func (e *Engine) Tokens() int {
	if e.acct == nil {
		return 0
	}
	return e.acct.Tokens()
}

// Brief — те, що пропонент бачить перед кожною спробою: чинна стратегія, як
// вона виміряна, і що вже пробували. Усе це береться з ПОВЕДІНКИ воркера й
// історії циклу; відповідей з eval-набору тут нема і бути не може.
func (e *Engine) Brief() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "Current strategy (score %.3f):\n<<<STRATEGY\n%s\n>>>\n\n", e.current, strings.TrimSpace(e.doc.Raw))
	fmt.Fprintf(&b, "How the current strategy behaved on the dev set:\n%s\n\n", e.detail)
	fmt.Fprintf(&b, "Allowed tools: %s\n", strings.Join(e.Allowlist, ", "))
	fmt.Fprintf(&b, "Experiments so far: %d\n", len(e.history))
	from := 0
	if len(e.history) > 6 {
		from = len(e.history) - 6
	}
	for _, h := range e.history[from:] {
		fmt.Fprintf(&b, "- #%d %s: %s (%s)\n", h.ID, h.Verdict, h.DiffSummary, h.Reason)
	}
	return b.String()
}

// FixtureScorer — детермінований scorer на фікстурах, для тестів Engine.
//
// Це НЕ той вимір, що в ДЗ: тут нема моделі, і тому тести циклу перевіряють
// логіку keep/discard, а не швидкість диска чи настрій провайдера. Реальний
// вимір — LLMScorer (agent.go).
type FixtureScorer struct {
	// Scores зіставляє інструкцію стратегії з балом.
	Scores map[string]float64
	// Default — бал для стратегії, якої немає в таблиці.
	Default float64
	// Cost — скільки «коштує» один вимір; Tick рухає годинник тесту.
	Cost time.Duration
	Tick func(time.Duration)
	// Tokens — скільки токенів «коштує» один вимір.
	Tokens int
}

// Score реалізує Scorer.
func (s *FixtureScorer) Score(ctx context.Context, candidate StrategyDoc) (Measurement, error) {
	if err := ctx.Err(); err != nil {
		return Measurement{Tokens: s.Tokens}, err
	}
	if s.Tick != nil && s.Cost > 0 {
		s.Tick(s.Cost)
	}
	m := Measurement{Score: s.Default, Tokens: s.Tokens, Detail: "fixture"}
	if v, ok := s.Scores[candidate.Instruction]; ok {
		m.Score = v
	}
	return m, nil
}
