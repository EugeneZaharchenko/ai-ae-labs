// Принцип 5: повний аудиторський слід.
//
// Дві речі, які роблять цей лог гальмом, а не логуванням:
//
//  1. **append-only за конструкцією**. AuditLog приймає io.Writer — не файл, не
//     шлях. У нього немає Seek, Truncate чи Rewrite, тому жодна гілка коду
//     фізично не може переписати минуле. У демо це файл, відкритий з
//     O_APPEND; у тестах — bytes.Buffer.
//  2. **лог лежить ПОЗА поверхнею змін агента**. strategy.md агент редагує,
//     experiments.log — ні (Surface.Resolve("experiments.log") відхиляє шлях).
//     Агент, який може відредагувати власну історію, не має аудиту — він має
//     наратив.
//
// І ще одне з лекції: пишемо і keep, і discard. Compliance цікавить не лише
// «як ви покращили», а й «як ви вирішили НЕ покращувати».
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Verdict — рішення щодо одного експерименту.
type Verdict string

const (
	// VerdictKeep — метрика зросла, кандидат став новим базовим станом.
	VerdictKeep Verdict = "keep"
	// VerdictDiscard — метрика не зросла, зміну відкочено.
	VerdictDiscard Verdict = "discard"
	// VerdictWouldKeep і VerdictWouldDiscard — вердикти L1-прогону
	// (`--report-only`). Цикл виміряв кандидата й записав, ЯК БИ він вирішив,
	// але поверхні змін не торкався. Окремі значення, а не прапорець збоку:
	// рядок логу мусить читатися однозначно й через півроку, коли ніхто вже не
	// пам'ятає, з якими ключами його запускали.
	VerdictWouldKeep    Verdict = "would_keep"
	VerdictWouldDiscard Verdict = "would_discard"
	// VerdictRefused — пропозицію відхилено ДО оцінки: вихід за поверхню змін,
	// зіпсована стратегія або критик поза allowlist. Окремий вердикт, бо це
	// інший клас події: не «гіпотеза не спрацювала», а «гальмо спрацювало».
	VerdictRefused Verdict = "refused"
	// VerdictProposerFailed — Proposer не зміг згенерувати пропозицію (модель
	// впала, вичерпалась тощо). Це ще один клас події, відмінний і від
	// discard (кандидат оцінили й відкинули), і від refused (гальмо
	// зупинило вже згенеровану пропозицію): тут пропозиції не було взагалі,
	// але зупинка циклу все одно мусить лишити слід в аудиті, а не мовчки
	// зникнути.
	VerdictProposerFailed Verdict = "proposer_failed"
)

// Entry — один запис аудиторського сліду (один рядок JSON у experiments.log).
type Entry struct {
	ID          int       `json:"id"`
	TS          time.Time `json:"ts"`
	Attempt     int       `json:"attempt"`
	Verdict     Verdict   `json:"verdict"`
	Reason      string    `json:"reason"`
	TargetPath  string    `json:"target_path"`
	DiffSummary string    `json:"diff_summary"`
	PrevScore   float64   `json:"prev_score"`
	NewScore    float64   `json:"new_score"`
	// Scored розрізняє «оцінили й вийшло стільки-то» і «до оцінки не дійшло».
	// Без цього прапорця NewScore=0 у refused-записі читався б як «метрика
	// впала до нуля», і лог брехав би тихо.
	Scored      bool   `json:"scored"`
	StrategySHA string `json:"strategy_sha256"`
	DurationMS  int64  `json:"duration_ms"`
	// EvalTokens і ProposerTokens — вартість експерименту з UsageMetadata
	// моделей. Окремо, бо відповідають на різні питання: перше — «скільки
	// коштує виміряти кандидата», друге — «скільки коштує його вигадати».
	EvalTokens     int `json:"eval_tokens"`
	ProposerTokens int `json:"proposer_tokens"`
	// Detail — короткий людиночитний опис вимірювання (які кейси просіли).
	Detail string `json:"detail,omitempty"`
}

// AuditLog — append-only приймач записів.
type AuditLog struct {
	mu   sync.Mutex
	w    io.Writer
	next int
}

// NewAuditLog створює лог поверх довільного io.Writer.
func NewAuditLog(w io.Writer) *AuditLog { return &AuditLog{w: w, next: 1} }

// NewAuditLogAfter продовжує вже наявний журнал: наступний запис отримає
// lastID+1.
//
// Без цього кожен новий процес починав би нумерацію з 1, і Replay чесно
// повідомляв би про «переставлений рядок» на другому ж прогоні. Інваріант
// цілісності належить ФАЙЛУ, а не одному запуску, тому продовження нумерації —
// частина контракту, а не зручність.
func NewAuditLogAfter(w io.Writer, lastID int) *AuditLog {
	if lastID < 0 {
		lastID = 0
	}
	return &AuditLog{w: w, next: lastID + 1}
}

// Append дописує запис, присвоюючи йому наступний ID, і повертає збережений
// варіант. ID генерує лог, а не викликач: нумерація — частина інваріанта
// цілісності, який перевіряє Replay.
func (l *AuditLog) Append(e Entry) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	e.ID = l.next
	data, err := json.Marshal(e)
	if err != nil {
		return Entry{}, fmt.Errorf("audit: серіалізація запису: %w", err)
	}
	if _, err := l.w.Write(append(data, '\n')); err != nil {
		return Entry{}, fmt.Errorf("audit: запис: %w", err)
	}
	l.next++
	return e, nil
}

// Count повертає кількість дописаних записів.
func (l *AuditLog) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.next - 1
}

// ReadEntries читає JSON-lines лог назад у записи.
func ReadEntries(r io.Reader) ([]Entry, error) {
	var out []Entry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("audit: рядок %d: %w", len(out)+1, err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("audit: читання: %w", err)
	}
	return out, nil
}

// ErrAuditBroken повідомляє, що лог не відтворює послідовний прогін: пропущений
// або переставлений запис, розрив у ланцюжку метрик.
var ErrAuditBroken = errors.New("аудиторський слід порушено")

// Replayed — стан прогону, відтворений з логу.
type Replayed struct {
	Baseline         float64
	Final            float64
	Keeps            int
	Discards         int
	Refusals         int
	DryRuns          int
	ProposerFailures int
	LastEntry        Entry
}

// AsDryRun перетворює бойовий вердикт на його L1-відповідник.
//
// Відмова лишається відмовою: якщо гальмо №1 зупинило шлях, це сталося
// по-справжньому, і пом'якшувати формулювання в звіті нема за що.
func AsDryRun(v Verdict) Verdict {
	switch v {
	case VerdictKeep:
		return VerdictWouldKeep
	case VerdictDiscard:
		return VerdictWouldDiscard
	default:
		return v
	}
}

// Replay відтворює прогін із логу й перевіряє його цілісність.
//
// Це і є практичне значення слова «auditable»: не «ми щось писали у файл», а
// «з файлу можна відновити, звідки почали, що пробували й де опинилися — і
// якщо запис зник, це видно».
//
// Інваріанти:
//   - ID ідуть 1, 2, 3, … без розривів;
//   - PrevScore кожного запису дорівнює NewScore попереднього keep (або
//     baseline, якщо keep ще не було) — тобто ланцюжок метрик замкнений.
func Replay(entries []Entry) (Replayed, error) {
	if len(entries) == 0 {
		return Replayed{}, fmt.Errorf("%w: порожній лог", ErrAuditBroken)
	}

	state := Replayed{Baseline: entries[0].PrevScore, Final: entries[0].PrevScore}
	for i, e := range entries {
		if e.ID != i+1 {
			return Replayed{}, fmt.Errorf("%w: запис #%d має id=%d (очікували %d) — "+
				"схоже, рядок видалено або переставлено", ErrAuditBroken, i+1, e.ID, i+1)
		}
		if e.PrevScore != state.Final {
			return Replayed{}, fmt.Errorf("%w: запис id=%d стартує з %.4f, а попередній стан %.4f",
				ErrAuditBroken, e.ID, e.PrevScore, state.Final)
		}
		switch e.Verdict {
		case VerdictKeep:
			state.Keeps++
			state.Final = e.NewScore
		case VerdictDiscard:
			state.Discards++
		case VerdictRefused:
			state.Refusals++
		case VerdictWouldKeep, VerdictWouldDiscard:
			// L1-запис нічого не змінював, тому ланцюжок метрик він теж не
			// рухає: state.Final лишається тим самим і наступний запис має
			// стартувати саме з нього.
			state.DryRuns++
		case VerdictProposerFailed:
			// Так само не рухає ланцюжок метрик: пропозиції не було, отже
			// нема що оцінювати чи відкочувати.
			state.ProposerFailures++
		default:
			return Replayed{}, fmt.Errorf("%w: запис id=%d має невідомий вердикт %q",
				ErrAuditBroken, e.ID, e.Verdict)
		}
		state.LastEntry = e
	}
	return state, nil
}

// contentSHA — короткий відбиток стану артефакту після експерименту. Дає змогу
// звірити «те, що зараз у strategy.md» із конкретним рядком логу.
func contentSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
