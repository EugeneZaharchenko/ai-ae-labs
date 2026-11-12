package main

import (
	"errors"
	"strings"
	"testing"
)

func TestParseVerdict(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		role    string
		wantErr string // "" = must be ok
	}{
		{"чистий JSON", `{"critic":"style","passed":true}`, RoleStyle, ""},
		{"провал з коментарем", `{"critic":"security","passed":false,"comments":["s[:4] панікує на коротких рядках"]}`, RoleSecurity, ""},
		{"фенс знімається", "```json\n{\"critic\":\"style\",\"passed\":true}\n```", RoleStyle, ""},
		{"проза до і після JSON", "Мій вердикт:\n{\"critic\":\"style\",\"passed\":true}\nДякую!", RoleStyle, ""},
		{"тільки проза", "Код нормальний, можна мержити.", RoleStyle, "JSON"},
		{"порожня відповідь", "", RoleStyle, "JSON"},
		{"зламаний JSON", `{"critic":"style","passed":tru`, RoleStyle, "JSON"},
		{"passed не bool", `{"critic":"style","passed":"yes"}`, RoleStyle, "схему"},
		{"немає passed", `{"critic":"style","comments":["x"]}`, RoleStyle, "обов'язкового"},
		{"немає critic", `{"passed":true}`, RoleStyle, "обов'язкового"},
		{"невідома роль", `{"critic":"boss","passed":true}`, RoleStyle, "невідома роль"},
		{"підміна ролі", `{"critic":"correctness","passed":true}`, RoleStyle, "чекали роль"},
		{"провал без коментарів", `{"critic":"style","passed":false}`, RoleStyle, "без жодного коментаря"},
		{"провал з порожніми коментарями", `{"critic":"style","passed":false,"comments":["  ",""]}`, RoleStyle, "без жодного коментаря"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v, err := ParseVerdict(tt.raw, tt.role)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v, хотіли успіх", err)
				}
				if v.Critic != tt.role {
					t.Fatalf("Critic = %q, хотіли %q", v.Critic, tt.role)
				}
				return
			}
			if !errors.Is(err, ErrBadVerdict) {
				t.Fatalf("err = %v, хотіли ErrBadVerdict", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %q, хотіли підрядок %q", err, tt.wantErr)
			}
		})
	}
}

func TestDecide(t *testing.T) {
	t.Parallel()
	pass := func(r string) Verdict { return Verdict{Critic: r, Passed: true} }
	tests := []struct {
		name       string
		verdicts   []Verdict
		wantAccept bool
		wantFB     []string
	}{
		{"усі за", []Verdict{pass(RoleCompiler), pass(RoleCorrectness), pass(RoleStyle), pass(RoleSecurity)}, true, nil},
		{"немає вердиктів — не приймаємо", nil, false, nil},
		{
			"один проти — його коментарі у фідбек",
			[]Verdict{pass(RoleCompiler), {Critic: RoleCorrectness, Comments: []string{"переповнення int64"}}, pass(RoleStyle)},
			false, []string{"[correctness] переповнення int64"},
		},
		{
			"коментарі того, хто ЗА, не йдуть у фідбек",
			[]Verdict{{Critic: RoleStyle, Passed: true, Comments: []string{"можна коротше"}}, {Critic: RoleSecurity, Comments: []string{"panic"}}},
			false, []string{"[security] panic"},
		},
		{
			"відхилений вердикт = провал із системним коментарем",
			[]Verdict{pass(RoleCompiler), rejectedVerdict(RoleStyle, ErrBadVerdict)},
			false, []string{"[style] [system] відповідь критика відхилено: невалідний вердикт"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, fb := decide(tt.verdicts)
			if got != tt.wantAccept {
				t.Fatalf("accept = %t, хотіли %t", got, tt.wantAccept)
			}
			if strings.Join(fb, "\n") != strings.Join(tt.wantFB, "\n") {
				t.Fatalf("feedback = %q, хотіли %q", fb, tt.wantFB)
			}
		})
	}
}
