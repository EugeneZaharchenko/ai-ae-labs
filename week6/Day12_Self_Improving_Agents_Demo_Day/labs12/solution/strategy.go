// Принцип 4: стратегія агента — окремий людино-читний артефакт.
//
// Це аналог `program.md` з karpathy/autoresearch: документ, у якому описано,
// ЯК агент шукає покращення, — окремо від коду, який агент мутує. Людина
// редагує його в PR, агент пропонує до нього дельти, і обидва читають той
// самий файл.
//
// Ключове рішення цього файлу: strategy.md **парситься й валідується**, а не
// приймається як довільний текст. Валідатор — це місце, де матеріалізуються ще
// два гальма з лекції:
//
//   - принцип 3 (єдина недвозначна метрика): секція `## Metric` мусить містити
//     рівно один рядок. Дві метрики — це вже Pareto-фронт, який неможливо
//     автоматично звести до keep/discard;
//   - гальмо «tool allowlist»: кожен інструмент у `## Tools` звіряється з
//     allowlist рантайму. Інструмент, який агент «вигадав» собі сам, воркер не
//     отримає — а з `## Tools` воркер будується буквально, тож прибрати
//     інструмент зі стратегії означає справді позбавити воркера його.

// Саме `## Instruction` — те, що модель-воркер читає як системну інструкцію, і
// головне, що агент покращує. Решта секцій — каркас, який валідатор тримає
// незмінним за формою.
//
// Зіпсована стратегія — це помилка, а не мовчазне ігнорування. Агент, що пише
// у свій control point сміття й не отримує відмови, за десяток ітерацій
// перетворює контроль на декорацію.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Помилки валідації стратегії. Розділені навмисно: у audit-лог іде причина
// відмови, і «нема секції Metric» та «інструмент не в allowlist» — це різні
// інциденти для того, хто читає лог у пʼятницю разом із security.
var (
	// ErrStrategyMalformed — документ не відповідає формату.
	ErrStrategyMalformed = errors.New("стратегія має некоректний формат")
	// ErrMetricNotSingle — метрик нуль або більше однієї (принцип 3).
	ErrMetricNotSingle = errors.New("стратегія мусить оголошувати рівно одну метрику")
	// ErrToolNotAllowed — інструмент поза allowlist рантайму.
	ErrToolNotAllowed = errors.New("інструмент не входить до allowlist")
)

// StrategyDoc — розібрана стратегія.
//
// Raw зберігається поряд із розібраними полями, бо в audit-лог і у файл іде
// саме оригінальний текст: людина має бачити те, що написав агент, а не
// відрендерений парсером переказ.
type StrategyDoc struct {
	Title       string
	Metric      string
	Tools       []string
	Instruction string
	MaxAttempts int
	Raw         string
}

// MaxInstructionRunes — стеля розміру мутабельної частини. Це не косметика:
// без неї «покращення» можна виграти, вставивши в інструкцію пів бази знань.
const MaxInstructionRunes = 1200

// Назви обовʼязкових секцій. Формат навмисно бідний: markdown-заголовок другого
// рівня плюс список. Що складніший формат control point, то більша ймовірність,
// що людина перестане його читати.
const (
	sectionMetric = "metric"
	sectionTools  = "tools"
	sectionInstr  = "instruction"
	sectionStop   = "stop conditions"
)

// ParseStrategy розбирає й валідує документ стратегії.
//
// allowlist — дозволені інструменти; порожній allowlist означає «жоден
// інструмент не дозволений», а не «дозволені всі». Це свідомий вибір fail-closed: помилка
// конфігурації має зупиняти цикл, а не відкривати його.
func ParseStrategy(raw string, allowlist []string) (StrategyDoc, error) {
	doc := StrategyDoc{Raw: raw}
	sections := map[string][]string{}

	var current string
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue
		case strings.HasPrefix(trimmed, "## "):
			current = strings.ToLower(strings.TrimSpace(trimmed[3:]))
			if _, seen := sections[current]; seen {
				return StrategyDoc{}, fmt.Errorf("%w: секція %q оголошена двічі",
					ErrStrategyMalformed, current)
			}
			sections[current] = nil
		case strings.HasPrefix(trimmed, "# "):
			if doc.Title != "" {
				return StrategyDoc{}, fmt.Errorf("%w: більше одного заголовка першого рівня",
					ErrStrategyMalformed)
			}
			doc.Title = strings.TrimSpace(trimmed[2:])
		default:
			if current == "" {
				// Текст до першої секції — це преамбула для людини; вона нічого
				// не змінює в поведінці й тому ігнорується свідомо.
				continue
			}
			sections[current] = append(sections[current], trimmed)
		}
	}

	for name := range sections {
		switch name {
		case sectionMetric, sectionTools, sectionInstr, sectionStop:
		default:
			// Невідома секція — це текст, який людина бачить у PR, а воркер
			// ніколи не читає: ідеальне місце, щоб сховати щось від рев'ю.
			return StrategyDoc{}, fmt.Errorf("%w: невідома секція %q", ErrStrategyMalformed, name)
		}
	}

	if doc.Title == "" {
		return StrategyDoc{}, fmt.Errorf("%w: немає заголовка `# ...`", ErrStrategyMalformed)
	}

	metrics := sections[sectionMetric]
	if len(metrics) != 1 {
		return StrategyDoc{}, fmt.Errorf("%w: знайдено %d (секція `## Metric`)",
			ErrMetricNotSingle, len(metrics))
	}
	doc.Metric = stripBullet(metrics[0])
	if doc.Metric == "" {
		return StrategyDoc{}, fmt.Errorf("%w: порожня назва метрики", ErrMetricNotSingle)
	}

	tools, ok := sections[sectionTools]
	if !ok || len(tools) == 0 {
		return StrategyDoc{}, fmt.Errorf("%w: секція `## Tools` порожня або відсутня",
			ErrStrategyMalformed)
	}
	allowed := make(map[string]bool, len(allowlist))
	for _, name := range allowlist {
		allowed[name] = true
	}
	seen := map[string]bool{}
	for _, line := range tools {
		name := stripBullet(line)
		if name == "" {
			continue
		}
		if !allowed[name] {
			return StrategyDoc{}, fmt.Errorf("%w: %q (дозволені: %s)",
				ErrToolNotAllowed, name, strings.Join(sortedKeys(allowed), ", "))
		}
		if seen[name] {
			return StrategyDoc{}, fmt.Errorf("%w: інструмент %q повторюється",
				ErrStrategyMalformed, name)
		}
		seen[name] = true
		doc.Tools = append(doc.Tools, name)
	}
	if len(doc.Tools) == 0 {
		return StrategyDoc{}, fmt.Errorf("%w: жодного інструмента не оголошено", ErrStrategyMalformed)
	}

	instr := strings.TrimSpace(strings.Join(sections[sectionInstr], "\n"))
	if instr == "" {
		return StrategyDoc{}, fmt.Errorf("%w: секція `## Instruction` порожня або відсутня",
			ErrStrategyMalformed)
	}
	if n := len([]rune(instr)); n > MaxInstructionRunes {
		return StrategyDoc{}, fmt.Errorf("%w: інструкція має %d символів, ліміт %d — "+
			"довга інструкція переписує агента, а не вдосконалює його",
			ErrStrategyMalformed, n, MaxInstructionRunes)
	}
	doc.Instruction = instr

	stop, ok := sections[sectionStop]
	if !ok {
		return StrategyDoc{}, fmt.Errorf("%w: немає секції `## Stop conditions`",
			ErrStrategyMalformed)
	}
	attempts, found := parseMaxAttempts(stop)
	if !found {
		return StrategyDoc{}, fmt.Errorf("%w: у `## Stop conditions` немає `max_attempts = N`",
			ErrStrategyMalformed)
	}
	if attempts < 1 {
		return StrategyDoc{}, fmt.Errorf("%w: max_attempts = %d, має бути ≥ 1",
			ErrStrategyMalformed, attempts)
	}
	doc.MaxAttempts = attempts

	return doc, nil
}

// stripBullet прибирає markdown-марковання списку: "- name", "1. name", "* name".
func stripBullet(line string) string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "- ")
	s = strings.TrimPrefix(s, "* ")
	if i := strings.Index(s, ". "); i > 0 && i <= 3 {
		if _, err := strconv.Atoi(s[:i]); err == nil {
			s = s[i+2:]
		}
	}
	// Коментар після назви інструмента ("kb_read — повний текст") відрізаємо:
	// у порівнянні стратегій має брати участь ідентифікатор, а не проза.
	if i := strings.IndexAny(s, " \t—"); i > 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// parseMaxAttempts шукає `max_attempts = N` у рядках секції Stop conditions.
func parseMaxAttempts(lines []string) (int, bool) {
	for _, line := range lines {
		l := strings.ToLower(line)
		i := strings.Index(l, "max_attempts")
		if i < 0 {
			continue
		}
		rest := l[i+len("max_attempts"):]
		rest = strings.TrimLeft(rest, " \t=:")
		digits := rest
		for j, r := range rest {
			if r < '0' || r > '9' {
				digits = rest[:j]
				break
			}
		}
		n, err := strconv.Atoi(strings.TrimSpace(digits))
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// sortedKeys повертає впорядкований список ключів — щоб текст помилки був
// однаковим від запуску до запуску (інакше тест на повідомлення флакає).
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RenderStrategy збирає канонічний текст стратегії. Використовується демо для
// створення нульового стану strategy.md і тестами — для побудови кандидатів.
func RenderStrategy(doc StrategyDoc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", doc.Title)
	fmt.Fprintf(&b, "## Metric\n%s\n\n", doc.Metric)
	b.WriteString("## Tools\n")
	for _, c := range doc.Tools {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	fmt.Fprintf(&b, "\n## Instruction\n%s\n", doc.Instruction)
	fmt.Fprintf(&b, "\n## Stop conditions\n- max_attempts = %d\n", doc.MaxAttempts)
	return b.String()
}
