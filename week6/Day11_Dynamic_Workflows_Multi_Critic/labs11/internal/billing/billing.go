// Package billing holds the work the Day 11 maker is asked to do: three small
// Go functions from LEDGERWORKS' billing service, each with a visible test file
// (the specification the loop's deterministic gate runs) and a held-out test
// file (used only by the eval harness).
//
// The split is the point. A loop that is graded by the same tests that steer it
// measures its ability to satisfy those tests, not its ability to write the
// function. The holdout is what catches "green on the visible tests, wrong on
// the case nobody wrote down" — the exact shape of bug #423 (a refund rounded
// through float64 that lost a cent on large amounts).
//
// Every Task also carries a Reference (passes both files) and a Buggy variant
// (passes the visible file, fails the holdout). internal tests grade both with
// the real toolchain: a task whose "wrong" solution is not actually caught is a
// broken eval, and that is worth a failing test.
package billing

// Task is one unit of work for the maker.
type Task struct {
	// ID is the stable key used in eval tables and test names.
	ID string
	// Prompt is the user message the agent receives. It names the file's
	// package and every exported identifier the tests use.
	Prompt string
	// Visible is the test file the gate runs inside the loop; the maker also
	// sees it, as a specification.
	Visible string
	// Holdout is run only by the eval harness, after the loop has stopped.
	Holdout string
	// Reference passes Visible and Holdout.
	Reference string
	// Buggy passes Visible but fails Holdout.
	Buggy string
	// Flaw says, in one sentence, what is wrong with Buggy — the comment a good
	// correctness critic would write. Offline demos script it; the live panel
	// has to find it on its own.
	Flaw string
}

// Tasks returns the task set in a stable order. Callers get a copy.
func Tasks() []Task {
	out := make([]Task, len(tasks))
	copy(out, tasks)
	return out
}

// ByID returns the task with the given ID.
func ByID(id string) (Task, bool) {
	for _, t := range tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}

var tasks = []Task{prorate, parseAmount, iban}

const header = `package billing

import (
	"errors"
	"testing"
)

`

var prorate = Task{
	ID: "prorate_refund",
	Prompt: `Напиши Go-функцію для billing-сервісу LEDGERWORKS (package billing, один файл, лише стандартна бібліотека).

  var ErrInvalid = errors.New("invalid input")
  func ProrateRefund(amountCents int64, daysUsed, periodDays int) (int64, error)

Повернення за невикористану частину періоду: amountCents * (periodDays-daysUsed) / periodDays, округлене до цілого цента half-up (0.5 цента округлюється вгору).
Помилка, що обгортає ErrInvalid, якщо: periodDays <= 0, daysUsed < 0, daysUsed > periodDays, amountCents < 0.
Сума може бути дуже великою: результат не має залежати від переповнення int64 чи від втрат точності float64.`,
	Visible: header + `
func TestProrateRefund(t *testing.T) {
	tests := []struct {
		amount     int64
		used, per  int
		want       int64
	}{
		{1000, 10, 30, 667},
		{1000, 0, 30, 1000},
		{1000, 30, 30, 0},
		{100, 1, 3, 67},
		{1, 1, 2, 1},
		{5, 1, 4, 4},
	}
	for _, tt := range tests {
		got, err := ProrateRefund(tt.amount, tt.used, tt.per)
		if err != nil || got != tt.want {
			t.Errorf("ProrateRefund(%d,%d,%d) = %d, %v; want %d", tt.amount, tt.used, tt.per, got, err, tt.want)
		}
	}
}

func TestProrateRefundInvalid(t *testing.T) {
	for _, c := range [][3]int64{{100, 1, 0}, {100, -1, 30}, {100, 31, 30}, {-5, 1, 30}} {
		if _, err := ProrateRefund(c[0], int(c[1]), int(c[2])); !errors.Is(err, ErrInvalid) {
			t.Errorf("ProrateRefund%v: err = %v, want ErrInvalid", c, err)
		}
	}
}
`,
	Holdout: header + `
func TestProrateRefundLargeAmounts(t *testing.T) {
	// bug #423: float64 and int64 multiplication both lose or wrap here.
	tests := []struct {
		amount    int64
		used, per int
		want      int64
	}{
		{999999999999999999, 0, 30, 999999999999999999},
		{9223372036854775807, 15, 30, 4611686018427387904},
		{9007199254740993, 1, 2, 4503599627370497},
	}
	for _, tt := range tests {
		got, err := ProrateRefund(tt.amount, tt.used, tt.per)
		if err != nil || got != tt.want {
			t.Errorf("ProrateRefund(%d,%d,%d) = %d, %v; want %d", tt.amount, tt.used, tt.per, got, err, tt.want)
		}
	}
}

func TestProrateRefundZeroAmountAndBoundaries(t *testing.T) {
	if got, err := ProrateRefund(0, 5, 30); err != nil || got != 0 {
		t.Errorf("zero amount = %d, %v", got, err)
	}
	if got, err := ProrateRefund(1, 0, 1); err != nil || got != 1 {
		t.Errorf("single-day period = %d, %v", got, err)
	}
	if _, err := ProrateRefund(100, 0, -1); !errors.Is(err, ErrInvalid) {
		t.Errorf("negative period: err = %v", err)
	}
}
`,
	Flaw: "float64 втрачає копійки на великих сумах (> 2^53), а int64-множення переповнюється: порахуй через math/big або bits.Mul64",
	Reference: `package billing

import (
	"errors"
	"math/big"
)

var ErrInvalid = errors.New("invalid input")

func ProrateRefund(amountCents int64, daysUsed, periodDays int) (int64, error) {
	if periodDays <= 0 || daysUsed < 0 || daysUsed > periodDays || amountCents < 0 {
		return 0, ErrInvalid
	}
	num := new(big.Int).Mul(big.NewInt(amountCents), big.NewInt(int64(periodDays-daysUsed)))
	den := big.NewInt(int64(periodDays))
	// half-up: floor((2*num + den) / (2*den))
	num.Mul(num, big.NewInt(2)).Add(num, den)
	num.Quo(num, new(big.Int).Mul(den, big.NewInt(2)))
	return num.Int64(), nil
}
`,
	Buggy: `package billing

import (
	"errors"
	"math"
)

var ErrInvalid = errors.New("invalid input")

func ProrateRefund(amountCents int64, daysUsed, periodDays int) (int64, error) {
	if periodDays <= 0 || daysUsed < 0 || daysUsed > periodDays || amountCents < 0 {
		return 0, ErrInvalid
	}
	return int64(math.Round(float64(amountCents) * float64(periodDays-daysUsed) / float64(periodDays))), nil
}
`,
}

var parseAmount = Task{
	ID: "parse_amount",
	Prompt: `Напиши Go-функцію для billing-сервісу LEDGERWORKS (package billing, один файл, лише стандартна бібліотека).

  var ErrInvalid = errors.New("invalid input")
  func ParseAmount(s string) (int64, error)

Розбирає суму у форматі виписок українських банків і повертає копійки (int64).
Правила: необов'язковий знак + або -; десятковий роздільник "," або "."; не більше двох цифр після нього;
пробіли, нерозривні пробіли (U+00A0) і вузькі нерозривні (U+202F) усередині числа — роздільники тисяч і ігноруються;
необов'язковий суфікс "грн" або "UAH" (у будь-якому регістрі) після числа. Усе інше — помилка, що обгортає ErrInvalid:
порожній рядок, експонента ("1e3"), кілька роздільників ("1,234.56"), три й більше знаків після коми, переповнення int64.
Тиха обрізка чи округлення забороненi: "1,234" — помилка, а не 123.`,
	Visible: header + `
func TestParseAmount(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"12", 1200},
		{"12,5", 1250},
		{"12.50", 1250},
		{"1 234,56", 123456},
		{"-7,05", -705},
		{"+3", 300},
		{"0,01", 1},
		{"  42 грн ", 4200},
	}
	for _, tt := range tests {
		got, err := ParseAmount(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("ParseAmount(%q) = %d, %v; want %d", tt.in, got, err, tt.want)
		}
	}
}

func TestParseAmountInvalid(t *testing.T) {
	for _, in := range []string{"", "abc", "1,2,3"} {
		if _, err := ParseAmount(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseAmount(%q): err = %v, want ErrInvalid", in, err)
		}
	}
}
`,
	Holdout: header + `
func TestParseAmountEdgeFormats(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"1 234,56", 123456},
		{"1 234 567.8", 123456780},
		{"-0,01", -1},
		{"19,99 UAH", 1999},
		{"7 uah", 700},
		{"0,29", 29},
		{"92233720368547758,07", 9223372036854775807},
	}
	for _, tt := range tests {
		got, err := ParseAmount(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("ParseAmount(%q) = %d, %v; want %d", tt.in, got, err, tt.want)
		}
	}
}

func TestParseAmountRejects(t *testing.T) {
	for _, in := range []string{"1e3", "1,234.56", "1,234", "92233720368547758,08", "-", ",5", "5,", "12 грн грн", "--5"} {
		if v, err := ParseAmount(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseAmount(%q) = %d, %v; want ErrInvalid", in, v, err)
		}
	}
}
`,
	Flaw: "ParseFloat приймає експоненту (1e3) і зайві формати, а ReplaceAll(\" \") не знає про U+00A0/U+202F; множення float на 100 — не гроші",
	Reference: `package billing

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var ErrInvalid = errors.New("invalid input")

var amountRe = regexp.MustCompile(` + "`" + `^([+-]?)(\d+)(?:[.,](\d{1,2}))?$` + "`" + `)

func ParseAmount(s string) (int64, error) {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	for _, suffix := range []string{"грн", "uah"} {
		if strings.HasSuffix(low, suffix) {
			s = strings.TrimSpace(s[:len(s)-len(suffix)])
			break
		}
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	m := amountRe.FindStringSubmatch(s)
	if m == nil {
		return 0, ErrInvalid
	}
	whole, err := strconv.ParseUint(m[2], 10, 64)
	if err != nil || whole > math.MaxInt64/100 {
		if err == nil && whole == math.MaxInt64/100 {
			// boundary handled below
		} else {
			return 0, ErrInvalid
		}
	}
	frac := uint64(0)
	if m[3] != "" {
		f := m[3]
		if len(f) == 1 {
			f += "0"
		}
		frac, _ = strconv.ParseUint(f, 10, 64)
	}
	total := whole*100 + frac
	if total > math.MaxInt64 {
		return 0, ErrInvalid
	}
	if m[1] == "-" {
		return -int64(total), nil
	}
	return int64(total), nil
}
`,
	Buggy: `package billing

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

var ErrInvalid = errors.New("invalid input")

func ParseAmount(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "грн")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, ",", ".")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, ErrInvalid
	}
	return int64(math.Round(f * 100)), nil
}
`,
}

const ibanPrefix = `package billing

import "testing"

`

var iban = Task{
	ID: "validate_iban",
	Prompt: `Напиши Go-функцію для billing-сервісу LEDGERWORKS (package billing, один файл, лише стандартна бібліотека).

  func IsValidIBAN(s string) bool

Перевіряє IBAN за ISO 13616 (mod-97). Пробіли всередині ігноруються, регістр не важливий.
Формат: 2 літери країни, 2 цифри контрольної суми, далі 11-30 букв/цифр (загальна довжина 15-34).
Контроль: перенести перші 4 символи в кінець, замінити літери на числа (A=10 … Z=35), остача від ділення на 97 має дорівнювати 1.
Для країни UA довжина рівно 29. Будь-які інші символи (дефіси, крапки, не-ASCII) — невалідний IBAN.
Число з 29+ цифр не вміщується в жоден цілий тип: рахуй остачу інкрементально.`,
	Visible: ibanPrefix + `
func TestIsValidIBAN(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"GB82 WEST 1234 5698 7654 32", true},
		{"DE89370400440532013000", true},
		{"GB82 WEST 1234 5698 7654 33", false},
		{"", false},
		{"DE89", false},
	}
	for _, tt := range tests {
		if got := IsValidIBAN(tt.in); got != tt.want {
			t.Errorf("IsValidIBAN(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
`,
	Holdout: ibanPrefix + `
func TestIsValidIBANHoldout(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"UA213223130000026007233566001", true},
		{"ua21 3223 1300 0002 6007 2335 6600 1", true},
		{"gb82west12345698765432", true},
		{"UA17322313000002600723356600", false},   // UA, mod-97 валідний, але 28 символів
		{"UA0732231300000260072335660013", false}, // UA, mod-97 валідний, але 30 символів
		{"DE89-3704-0044-0532-0130-00", false},
		{"DE89370400440532013000 ", false},
		{"ДЕ89370400440532013000", false},
		{"1289370400440532013000", false},
		{"DE8937040044053201300", false},
	}
	for _, tt := range tests {
		if got := IsValidIBAN(tt.in); got != tt.want {
			t.Errorf("IsValidIBAN(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
`,
	Flaw: "немає перевірки довжини для UA (рівно 29) і формату країни/контрольних цифр: mod-97 сам по собі пропускає IBAN хибної довжини",
	Reference: `package billing

import "strings"

func IsValidIBAN(s string) bool {
	s = strings.ToUpper(strings.ReplaceAll(s, " ", ""))
	if len(s) < 15 || len(s) > 34 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
			if i < 2 {
				return false
			}
		default:
			return false
		}
	}
	if s[2] < '0' || s[2] > '9' || s[3] < '0' || s[3] > '9' {
		return false
	}
	if s[:2] == "UA" && len(s) != 29 {
		return false
	}
	rem := 0
	for _, c := range s[4:] + s[:4] {
		if c >= 'A' {
			rem = (rem*100 + int(c-'A') + 10) % 97
		} else {
			rem = (rem*10 + int(c-'0')) % 97
		}
	}
	return rem == 1
}
`,
	Buggy: `package billing

import "strings"

func IsValidIBAN(s string) bool {
	s = strings.ToUpper(strings.ReplaceAll(s, " ", ""))
	if len(s) < 15 || len(s) > 34 {
		return false
	}
	rem := 0
	for _, c := range s[4:] + s[:4] {
		switch {
		case c >= 'A' && c <= 'Z':
			rem = (rem*100 + int(c-'A') + 10) % 97
		case c >= '0' && c <= '9':
			rem = (rem*10 + int(c-'0')) % 97
		default:
			return false
		}
	}
	return rem == 1
}
`,
}
