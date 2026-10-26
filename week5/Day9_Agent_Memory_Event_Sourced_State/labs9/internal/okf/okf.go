// Package okf — мінімальний OKF-store: кураторське знання в репозиторії.
//
// Це п'ятий шар пам'яті — той, який лендінг курсу обіцяє як «LLM Wiki Memory».
// Ідея (Karpathy, LLM Wiki; формалізовано Google в Open Knowledge Format):
// замість того, щоб щоразу робити retrieval по сирих джерелах, агент
// ІНКРЕМЕНТАЛЬНО підтримує кураторську вікі з Markdown-сторінок.
//
// Три речі, які тут несуть навантаження:
//
//	raw/ vs wiki/  — межа довіри. raw/ — ground truth, агент його ЛИШЕ читає.
//	                 wiki/ — єдина поверхня, куди агент має право писати. Це
//	                 обмежує blast radius помилки агента до «зіпсував вікі».
//	index.md       — детермінований каталог: той самий набір сторінок завжди
//	                 дає байт-у-байт той самий index.md.
//	log.md         — append-only журнал операцій. Той самий принцип, що й
//	                 StateDelta, тільки на рівні файлів.
//
// Станом на 08/2026 OKF — v0.2, і в ній `timestamp` перейменовано на
// `generated.at`. Ми пишемо `generated.at` і читаємо обидва: реальні репозиторії
// містять сторінки, згенеровані ще за v0.1.
//
// ВАЖЛИВО щодо залежностей: у дозволеному бюджеті цього модуля немає
// YAML-бібліотеки, тож парсер frontmatter написаний вручну і підтримує рівно
// той підмножинний синтаксис, який потрібен OKF: скалярні рядки та inline-список
// `tags: [a, b]`. Це навчальний парсер, не заміна gopkg.in/yaml.v3.
package okf

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Помилки межі довіри.
var (
	// ErrReadOnlySurface — спроба запису поза wiki/ (наприклад, у raw/).
	ErrReadOnlySurface = errors.New("okf: write outside the mutable wiki/ surface")
	// ErrReservedName — спроба записати зарезервоване ім'я звичайним записом.
	ErrReservedName = errors.New("okf: reserved file name")
	// ErrMissingType — сторінка без обов'язкового поля type.
	ErrMissingType = errors.New("okf: frontmatter is missing the required 'type' field")
)

// Зарезервовані OKF-імена: index.md генерується, log.md дописується.
const (
	IndexFile = "index.md"
	LogFile   = "log.md"
)

// Page — одна OKF-сторінка.
type Page struct {
	// Type — ЄДИНЕ обов'язкове поле специфікації.
	Type string
	// Решта — опційні.
	Title       string
	Description string
	Resource    string // посилання на source of truth у raw/ або в коді
	Tags        []string
	GeneratedAt string // OKF v0.2; у v0.1 поле звалося timestamp
	Body        string
}

// Store — вікі-бандл на файловій системі: <root>/raw (read-only) і
// <root>/wiki (mutable).
type Store struct{ root string }

// NewStore створює бандл. У тестах root — це t.TempDir(); справжня вікі
// репозиторію ніколи не є ціллю цього коду.
func NewStore(root string) (*Store, error) {
	s := &Store{root: root}
	for _, d := range []string{s.rawDir(), s.wikiDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("okf: mkdir %s: %w", d, err)
		}
	}
	return s, nil
}

func (s *Store) rawDir() string  { return filepath.Join(s.root, "raw") }
func (s *Store) wikiDir() string { return filepath.Join(s.root, "wiki") }

// WriteRaw кладе ground-truth документ у raw/.
//
// Це операція КОНВЕЄРА ПРИЙОМУ, а не агента: raw/ наповнює пул джерел
// (завантажувач, людина, cron), і саме тому вона тут окремим методом, а не
// частиною Store-API, доступного інструментам агента.
func (s *Store) WriteRaw(name string, content []byte) error {
	p, err := safeJoin(s.rawDir(), name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, content, 0o644)
}

// ReadRaw читає ground truth. Єдина операція агента над raw/ — читання.
func (s *Store) ReadRaw(name string) ([]byte, error) {
	p, err := safeJoin(s.rawDir(), name)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// WritePage записує сторінку у wiki/ — єдина мутабельна поверхня агента.
//
// Три перевірки, і кожна відповідає рядку лекції:
//   - ім'я не має виводити за межі wiki/ (спроба «../raw/...» — це вихід за
//     межу довіри, а не помилка шляху);
//   - index.md і log.md зарезервовані: перший генерується, другий дописується;
//   - type обов'язковий за специфікацією OKF.
func (s *Store) WritePage(name string, p Page) error {
	if p.Type == "" {
		return ErrMissingType
	}
	base := path.Base(filepath.ToSlash(name))
	if base == IndexFile || base == LogFile {
		return fmt.Errorf("%w: %s is generated/append-only, not written directly", ErrReservedName, base)
	}
	full, err := safeJoin(s.wikiDir(), name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(RenderPage(p)), 0o644)
}

// ReadPage читає й парсить сторінку з wiki/.
func (s *Store) ReadPage(name string) (Page, error) {
	full, err := safeJoin(s.wikiDir(), name)
	if err != nil {
		return Page{}, err
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return Page{}, err
	}
	return ParsePage(b)
}

// PageNames повертає відсортовані шляхи сторінок (без index.md і log.md).
func (s *Store) PageNames() ([]string, error) {
	var out []string
	err := filepath.WalkDir(s.wikiDir(), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		rel, err := filepath.Rel(s.wikiDir(), p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == IndexFile || rel == LogFile {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(out)
	return out, nil
}

// RegenIndex перегенеровує index.md.
//
// Детермінізм тут — вимога специфікації, і його легко зламати випадково:
// достатньо покласти в каталог час генерації або обійти мапу без сортування.
// Ми не робимо ані того, ані іншого, і тест на ідемпотентність це фіксує.
func (s *Store) RegenIndex() error {
	names, err := s.PageNames()
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("---\ntype: index\ntitle: Wiki index\n---\n\n# Індекс\n\n")
	for _, n := range names {
		p, err := s.ReadPage(n)
		if err != nil {
			return fmt.Errorf("okf: index %s: %w", n, err)
		}
		title := p.Title
		if title == "" {
			title = n
		}
		fmt.Fprintf(&b, "- [%s](%s) — %s\n", title, n, p.Type)
	}
	return os.WriteFile(filepath.Join(s.wikiDir(), IndexFile), []byte(b.String()), 0o644)
}

// AppendLog дописує рядок у log.md. Відкриття рівно з O_APPEND — жодного
// шляху, яким цей метод міг би скоротити або переписати журнал.
func (s *Store) AppendLog(at time.Time, op, subject, note string) error {
	f, err := os.OpenFile(filepath.Join(s.wikiDir(), LogFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "- %s `%s` %s — %s\n", at.UTC().Format(time.RFC3339), op, subject, note)
	return err
}

// ReadLog читає журнал цілком (для аудиту й тестів).
func (s *Store) ReadLog() (string, error) {
	b, err := os.ReadFile(filepath.Join(s.wikiDir(), LogFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Hit — сторінка вікі, що збіглась із запитом.
type Hit struct {
	Name   string // відносне ім'я сторінки в wiki/
	Title  string
	Text   string // description або перші слова тіла
	Source string // «okf:wiki/<name>» — походження для provenance
}

// Search — примітивний підстроковий пошук по сторінках вікі.
//
// Сторінки вікі пройшли людський рев'ю в PR, тому вони довіреніші за репліку
// в чаті — але це все одно ПРОПОЗИЦІЯ контексту, а не інструкція.
func (s *Store) Search(query string) ([]Hit, error) {
	names, err := s.PageNames()
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var out []Hit
	for _, n := range names {
		p, err := s.ReadPage(n)
		if err != nil {
			return nil, err
		}
		hay := strings.ToLower(n + " " + p.Title + " " + p.Description + " " + strings.Join(p.Tags, " ") + " " + p.Body)
		if q != "" && !strings.Contains(hay, q) {
			continue
		}
		text := p.Description
		if text == "" {
			text = firstWords(p.Body, 12)
		}
		out = append(out, Hit{Name: n, Title: p.Title, Text: text, Source: "okf:wiki/" + n})
	}
	return out, nil
}

func firstWords(s string, n int) string {
	w := strings.Fields(s)
	if len(w) <= n {
		return strings.Join(w, " ")
	}
	return strings.Join(w[:n], " ") + "…"
}

// safeJoin не дає імені вийти за межі базового каталогу.
//
// Це та сама межа довіри, тільки виражена в коді: без цієї перевірки
// WritePage("../raw/ground-truth.md", …) переписав би ground truth, і жоден
// коментар у README цього б не зупинив.
func safeJoin(base, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: empty name", ErrReadOnlySurface)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("%w: %q is absolute", ErrReadOnlySurface, name)
	}
	full := filepath.Join(base, clean)
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q escapes %s", ErrReadOnlySurface, name, base)
	}
	return full, nil
}

// --- frontmatter ------------------------------------------------------------

// RenderPage серіалізує сторінку в Markdown із YAML-frontmatter.
// Порядок полів фіксований — інакше index/сторінки не були б відтворюваними.
func RenderPage(p Page) string {
	var b strings.Builder
	b.WriteString("---\n")
	writeField(&b, "type", p.Type)
	writeField(&b, "title", p.Title)
	writeField(&b, "description", p.Description)
	writeField(&b, "resource", p.Resource)
	if len(p.Tags) > 0 {
		fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(p.Tags, ", "))
	}
	writeField(&b, "generated.at", p.GeneratedAt)
	b.WriteString("---\n\n")
	b.WriteString(p.Body)
	if !strings.HasSuffix(p.Body, "\n") {
		b.WriteByte('\n')
	}
	return b.String()
}

func writeField(b *strings.Builder, key, val string) {
	if val == "" {
		return
	}
	fmt.Fprintf(b, "%s: %s\n", key, quoteIfNeeded(val))
}

// quoteIfNeeded бере значення в лапки, коли інакше YAML прочитався б інакше
// (двокрапка робить значення мапою, решта символів — коментарем чи списком).
func quoteIfNeeded(v string) string {
	if strings.ContainsAny(v, ":#[]{}\"") || strings.TrimSpace(v) != v {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
	}
	return v
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(v[1 : len(v)-1])
	}
	return v
}

// ParsePage розбирає Markdown з frontmatter у Page.
//
// Приймає і `generated.at` (OKF v0.2), і застарілий `timestamp` (v0.1) —
// реальні бандли змішані, а падати на чужій версії формату для читача гірше,
// ніж тихо його зрозуміти.
func ParsePage(raw []byte) (Page, error) {
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return Page{}, fmt.Errorf("okf: page does not start with a '---' frontmatter fence")
	}

	var p Page
	closed := false
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "type":
			p.Type = unquote(val)
		case "title":
			p.Title = unquote(val)
		case "description":
			p.Description = unquote(val)
		case "resource":
			p.Resource = unquote(val)
		case "tags":
			p.Tags = parseInlineList(val)
		case "generated.at", "timestamp":
			p.GeneratedAt = unquote(val)
		}
	}
	if !closed {
		return Page{}, fmt.Errorf("okf: unterminated frontmatter")
	}

	var body strings.Builder
	for sc.Scan() {
		body.WriteString(sc.Text())
		body.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return Page{}, err
	}
	p.Body = strings.TrimLeft(body.String(), "\n")

	if p.Type == "" {
		return p, ErrMissingType
	}
	return p, nil
}

func parseInlineList(v string) []string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "[")
	v = strings.TrimSuffix(v, "]")
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := unquote(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
