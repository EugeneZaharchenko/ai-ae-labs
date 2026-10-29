// Тиждень 6, частина 2 — Self-Improving Agents (патерн karpathy/autoresearch).
//
// Цей файл — гальмо №1: **обмежена поверхня змін**. Агент має право змінити
// рівно ОДИН оголошений артефакт. Усе інше — read-only, і спроба вийти за межі
// має бути *відхилена*, а не «не рекомендована».
//
// Чому це окремий тип, а не перевірка `strings.HasPrefix` у місці запису (як у
// чернетці з лекції): префіксна перевірка ловить наївний випадок і пропускає
// три реальні — `..`-traversal, абсолютний шлях і символьне посилання, яке
// вказує назовні. Гарантія, яку легко обійти, — це не гарантія, а коментар.
//
// ADK-шар цього файлу не імпортує: поверхню змін можна протестувати без моделі,
// без ключа й без агента.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrOutsideSurface повідомляє, що шлях лежить поза дозволеною поверхнею змін.
//
// Це sentinel, а не рядок: виклик у Engine.Step перевіряє його через
// errors.Is, а тест — так само, тож формулювання можна змінювати, не ламаючи
// контракт.
var ErrOutsideSurface = errors.New("шлях поза дозволеною поверхнею змін")

// Surface — єдиний мутабельний артефакт агента всередині робочої директорії.
//
// root завжди symlink-resolved: на macOS t.TempDir() віддає /var/folders/...,
// що насправді є /private/var/folders/..., і без нормалізації кожне порівняння
// шляхів давало б хибне «поза межами».
type Surface struct {
	root string // абсолютний, symlink-resolved корінь робочої директорії
	rel  string // єдиний дозволений відносний шлях, напр. "strategy.md"
	abs  string // root/rel
}

// NewSurface оголошує поверхню змін: каталог root і рівно один файл rel у ньому.
//
// rel мусить бути відносним і без `..` — інакше сама *декларація* поверхні вже
// вивела б агента за межі робочої директорії, і всі подальші перевірки були б
// театром.
func NewSurface(root, rel string) (*Surface, error) {
	if root == "" {
		return nil, errors.New("surface: порожній root; робоча директорія обовʼязкова")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("surface: абсолютний шлях root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("surface: root %q недоступний: %w", root, err)
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("surface: root %q не є каталогом", root)
	}

	clean := filepath.Clean(rel)
	if rel == "" || filepath.IsAbs(clean) || clean == "." ||
		clean == ".." || hasDotDot(clean) {
		return nil, fmt.Errorf("surface: %q не є відносним шляхом усередині root", rel)
	}

	return &Surface{root: resolved, rel: clean, abs: filepath.Join(resolved, clean)}, nil
}

// hasDotDot повідомляє, чи виводить очищений шлях за межі свого кореня.
//
// filepath.Clean згортає всі внутрішні `..` («a/../b» → «b»), тож після Clean
// сегмент `..` може лишитися тільки на початку — і саме він означає вихід угору.
func hasDotDot(clean string) bool {
	return clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

// Rel повертає відносний шлях єдиного мутабельного артефакту.
func (s *Surface) Rel() string { return s.rel }

// Root повертає корінь робочої директорії (symlink-resolved).
func (s *Surface) Root() string { return s.root }

// Path повертає абсолютний шлях єдиного мутабельного артефакту.
func (s *Surface) Path() string { return s.abs }

// Resolve перевіряє шлях, у який агент просить записати, і повертає його
// абсолютну форму — або ErrOutsideSurface.
//
// Порядок перевірок навмисний: спершу нормалізуємо (Clean прибирає `a/../b`),
// потім розгортаємо симлінки *каталогу* (інакше `link/strategy.md`, де link →
// /tmp/evil, проходив би як «той самий базовий файл»), і лише тоді порівнюємо
// з єдиним дозволеним шляхом. Останньою йде перевірка самого артефакту: він
// має бути звичайним файлом, бо симлінк на місці strategy.md перетворює
// «дозволений запис» на запис у будь-що.
func (s *Surface) Resolve(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("%w: порожній шлях", ErrOutsideSurface)
	}

	cand := p
	if !filepath.IsAbs(cand) {
		cand = filepath.Join(s.root, cand)
	}
	cand = filepath.Clean(cand)

	if dir, err := filepath.EvalSymlinks(filepath.Dir(cand)); err == nil {
		cand = filepath.Join(dir, filepath.Base(cand))
	}

	if cand != s.abs {
		return "", fmt.Errorf("%w: %q; дозволено лише %q", ErrOutsideSurface, p, s.rel)
	}

	if fi, err := os.Lstat(cand); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: %q — символьне посилання, а не звичайний файл",
			ErrOutsideSurface, s.rel)
	}
	return cand, nil
}

// Read читає поточний вміст мутабельного артефакту.
func (s *Surface) Read() ([]byte, error) {
	path, err := s.Resolve(s.rel)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// Write перезаписує мутабельний артефакт. Використовується і для застосування
// кандидата, і для відкату (revert) — це та сама операція.
func (s *Surface) Write(content []byte) error {
	return s.WritePath(s.rel, content)
}

// WritePath записує content за шляхом p, попередньо провівши його через
// Resolve. Це єдиний шлях запису, яким користується Engine та ADK-інструмент.
//
// Продакшн-застереження, яке варто вимовити вголос: між Resolve і os.WriteFile
// існує вікно TOCTOU — процес, що виконується паралельно, може підмінити файл
// симлінком саме в цю мить. У Go без `openat`/`O_NOFOLLOW` цю щілину не
// закрити портабельно, і саме тому реальна ізоляція живе рівнем нижче — gVisor
// або Firecracker microVM (Курс 2), а не у перевірці шляху всередині процесу.
func (s *Surface) WritePath(p string, content []byte) error {
	path, err := s.Resolve(p)
	if err != nil {
		return err
	}
	return atomicWriteFile(path, content, 0o644)
}

// atomicWriteFile пише content у тимчасовий файл поруч із path, fsync'ає
// його і лише тоді атомарно перейменовує на місце path.
//
// Навіщо: `Engine.Step` пише кандидата на диск ДО оцінки і може відкотити
// його одразу після (write → score → write-back). Голий os.WriteFile лишає
// вікно, де крах процесу посеред запису лишає на диску частково записаний
// файл — і жодного запису про це в experiments.log, бо finish() ще не
// встиг відпрацювати. os.Rename на тому самому томі — атомарна операція
// файлової системи: після неї path або має старий вміст, або новий, і
// ніколи — половину.
func atomicWriteFile(path string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("surface: тимчасовий файл: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op після успішного Rename нижче

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("surface: запис у тимчасовий файл: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("surface: fsync тимчасового файлу: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("surface: закриття тимчасового файлу: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("surface: права доступу тимчасового файлу: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("surface: атомарне перейменування: %w", err)
	}
	return nil
}
