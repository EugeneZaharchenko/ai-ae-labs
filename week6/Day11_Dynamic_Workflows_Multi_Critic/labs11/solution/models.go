// Вибір моделей для maker'а й кожного критика.
//
// Одна й та сама модель як автор і як суддя — це відома пастка (self-preference
// bias: суддя віддає перевагу текстам, схожим на його власні). Тому кожна роль
// може мати свою модель, а збіг maker/критик не ховається, а друкується
// попередженням. Вибір іде через internal/modelcfg — той самий, що в лабах
// тижня 1: змінна LAB11_<РОЛЬ>_MODEL має той самий формат, що MODEL.
package main

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/adkenv"
	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
)

// Models — набір моделей одного прогону та пояснення вибору кожної.
type Models struct {
	Maker   model.LLM
	Critics map[Role]model.LLM
	// Choices — що вибрано і чому, за ключем "maker" або іменем ролі.
	Choices map[string]modelcfg.Choice
	// Warnings — діагностика конфігурації панелі (не помилки).
	Warnings []string
}

// roleEnv — змінні оточення, що перевизначають модель ролі.
var roleEnv = map[string]string{
	"maker":                 "LAB11_MAKER_MODEL",
	string(RoleCorrectness): "LAB11_CORRECTNESS_MODEL",
	string(RoleStyle):       "LAB11_STYLE_MODEL",
	string(RolePerformance): "LAB11_PERFORMANCE_MODEL",
}

// Resolver — залежності вибору моделей, підмінні в тестах.
type Resolver struct {
	// Lookup читає змінну оточення; порожнє значення = не задано.
	Lookup func(name string) (string, bool)
	// Default будує модель за замовчуванням (MODEL / DEFAULT_MODEL_PROVIDER / ключі).
	Default func(ctx context.Context) (model.LLM, modelcfg.Choice, error)
	// Build будує модель за явним вибором.
	Build func(ctx context.Context, provider, name string) (model.LLM, error)
}

// DefaultResolver — продакшн-залежності: apps/.env + modelcfg.
func DefaultResolver() Resolver {
	return Resolver{Lookup: adkenv.Key, Default: modelcfg.Load, Build: modelcfg.Build}
}

// Resolve вибирає моделі для maker'а і трьох критиків.
//
// Помилка тут — це «жодного провайдера не налаштовано» або «ключ не підійшов»;
// викликач сам вирішує, що робити (main падає в офлайн-демо і називає причину).
func (r Resolver) Resolve(ctx context.Context) (Models, error) {
	out := Models{Critics: map[Role]model.LLM{}, Choices: map[string]modelcfg.Choice{}}

	// Спершу модель за замовчуванням: вона ж — відповідь «чому саме ця».
	var def model.LLM
	var defChoice modelcfg.Choice
	pick := func(role string) (model.LLM, modelcfg.Choice, error) {
		if v, ok := r.Lookup(roleEnv[role]); ok {
			c, err := modelcfg.Choose(v, "")
			if err != nil {
				return nil, c, fmt.Errorf("%s: %w", roleEnv[role], err)
			}
			m, err := r.Build(ctx, c.Provider, c.Model)
			if err != nil {
				return nil, c, fmt.Errorf("%s: модель %q: %w", roleEnv[role], c.Model, err)
			}
			return m, c, nil
		}
		if def == nil {
			m, c, err := r.Default(ctx)
			if err != nil {
				return nil, c, err
			}
			def, defChoice = m, c
		}
		return def, defChoice, nil
	}

	var err error
	if out.Maker, out.Choices["maker"], err = pick("maker"); err != nil {
		return Models{}, err
	}
	for _, role := range []Role{RoleCorrectness, RoleStyle, RolePerformance} {
		m, c, err := pick(string(role))
		if err != nil {
			return Models{}, err
		}
		out.Critics[role] = m
		out.Choices[string(role)] = c
	}
	out.Warnings = selfPreferenceWarnings(out.Choices)
	return out, nil
}

// selfPreferenceWarnings називає критиків, що сидять на моделі maker'а.
func selfPreferenceWarnings(choices map[string]modelcfg.Choice) []string {
	maker := choices["maker"].Model
	var warns []string
	for _, role := range []Role{RoleCorrectness, RoleStyle, RolePerformance} {
		if choices[string(role)].Model == maker {
			warns = append(warns, fmt.Sprintf(
				"критик %s працює на тій самій моделі (%s), що й maker: суддя схильний схвалювати схожий на власний текст (self-preference). Задайте %s",
				role, maker, roleEnv[string(role)]))
		}
	}
	return warns
}

// Describe друкує вибір по ролях: що, на якому провайдері, чому.
func (m Models) Describe() string {
	var b strings.Builder
	for _, k := range []string{"maker", string(RoleCorrectness), string(RoleStyle), string(RolePerformance)} {
		c := m.Choices[k]
		fmt.Fprintf(&b, "  %-12s %s / %s  (%s)\n", k, c.Provider, c.Model, c.Reason)
	}
	for _, w := range m.Warnings {
		fmt.Fprintf(&b, "  УВАГА: %s\n", w)
	}
	return b.String()
}
