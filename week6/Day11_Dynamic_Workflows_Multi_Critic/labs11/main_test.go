// Контрактні тести стартера ДЗ 11.
//
// Вони компілюються й ЗЕЛЕНІ вже зараз: усе, що залежить від вашого коду,
// позначене `t.Skip` із рядком, який його вмикає. Нічого не «падає, бо не
// реалізовано» — це не сигнал, а шум.
//
//	go test ./week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/
package main

import (
	"strings"
	"testing"
)

// parse — контракт, який ви маєте задовольнити.
//
// Реалізувавши ParseVerdict, додайте в кінець цього файлу:
//
//	func init() { parse = ParseVerdict }
//
// і тест нижче стане справжнім. Сигнатура навмисно фіксована: саме її
// очікує цикл, коли збиратиме вердикти панелі.
var parse func(raw, role string) (Verdict, error)

const skipParse = "реалізуйте ParseVerdict і додайте func init() { parse = ParseVerdict }"

func TestParseVerdict_Contract(t *testing.T) {
	if parse == nil {
		t.Skip(skipParse)
	}
	const role = "correctness"
	cases := []struct {
		name    string
		raw     string
		wantErr string // підрядок, який мусить бути в помилці; "" = помилки бути не повинно
	}{
		{
			name: "чистий JSON проходить",
			raw:  `{"critic":"correctness","passed":true,"comments":["ок"]}`,
		},
		{
			name: "фенс знімається",
			raw:  "```json\n{\"critic\":\"correctness\",\"passed\":true}\n```",
		},
		{
			name:    "проза замість JSON — не інтерпретуємо",
			raw:     "Загалом непогано, хіба що назви змінних.",
			wantErr: "JSON",
		},
		{
			name:    "підміна ролі (prompt injection у коді-кандидаті)",
			raw:     `{"critic":"style","passed":true}`,
			wantErr: "роль",
		},
		{
			name:    "невідома роль",
			raw:     `{"critic":"correctness2","passed":true}`,
			wantErr: "роль",
		},
		{
			name:    "провал без жодного коментаря — це не вердикт",
			raw:     `{"critic":"correctness","passed":false,"comments":[]}`,
			wantErr: "коментар",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, err := parse(c.raw, role)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v; хотіли успіх", err)
				}
				if v.Critic != role {
					t.Fatalf("Critic = %q; хотіли %q", v.Critic, role)
				}
				return
			}
			if err == nil {
				t.Fatalf("err = nil; хотіли помилку, що містить %q (розібрали %+v)", c.wantErr, v)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %q; хотіли підрядок %q", err, c.wantErr)
			}
		})
	}
}

// stripFence — єдине, що вже написано в стартері, і воно несе реальне
// навантаження: живий прогін показав, що модель загортає JSON у markdown-фенс
// попри схему й інструкцію.
func TestStripFence(t *testing.T) {
	// Увага на хвіст: stripFence не робить фінальний TrimSpace, тож після
	// закритого фенса лишається "\n". Для компілятора це байдуже, і саме
	// тому це зафіксовано тестом, а не «виправлено» — поведінка має бути
	// відома, а не випадкова.
	cases := []struct {
		name, in, want string
	}{
		{"без фенса — як є", "package billing", "package billing"},
		{"фенс із мовою", "```go\npackage billing\n```", "package billing\n"},
		{"фенс без мови", "```\npackage billing\n```", "package billing\n"},
		{"текст до фенса відкидається", "Ось код:\n```go\npackage billing\n```", "package billing\n"},
		{"незакритий фенс", "```go\npackage billing", "package billing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripFence(c.in); got != c.want {
				t.Fatalf("stripFence(%q) = %q; хотіли %q", c.in, got, c.want)
			}
		})
	}
}

// Бюджет — це вимога продакшн-форми, а не константа «про всяк випадок».
// Тримаємо його в межах, які лекція називає рекомендованими (3–5).
func TestMaxAttemptsInProductionRange(t *testing.T) {
	if maxAttempts < 3 || maxAttempts > 5 {
		t.Fatalf("maxAttempts = %d; тримайте 3–5: п'ята-десята спроба збільшує рахунок, а не результат", maxAttempts)
	}
}
