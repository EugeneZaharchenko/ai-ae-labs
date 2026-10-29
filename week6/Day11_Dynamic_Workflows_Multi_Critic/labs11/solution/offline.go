// Офлайн-режим: скриптовані моделі, коли жоден провайдер не налаштований.
//
// Це не «фейкова лаба», а режим, у якому видно ЦИКЛ без ключа: gate, що
// запускає справжні go build/vet/test, і ADK-граф — ті самі, що й з живою
// моделлю. Скриптовані лише відповіді моделей, і сценарій — той самий, який
// вчить частину: заглушка → зелене на видимих тестах, але хибне → правильне.
package main

import (
	"fmt"

	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/billing"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/usage"
)

// stubCode — перша чернетка сценарію: не збирається, її ріже грейдер.
const stubCode = "package billing\n\n// TODO: реалізувати\n"

// OfflineModels повертає скриптовані моделі для завдання:
//
//	спроба 1: заглушка            → грейдер (build), критики НЕ викликаються
//	спроба 2: Buggy               → зелене на видимих тестах, correctness: passed=false
//	спроба 3: Reference           → панель одностайна
func OfflineModels(task billing.Task) Models {
	script := func(r Role, passed bool, comments ...string) fakellm.Turn {
		return fakellm.TextTurn(verdictText(r, passed, comments...))
	}
	critic := func(name string, turns ...fakellm.Turn) model.LLM {
		return usage.Estimating(fakellm.New(name, turns...))
	}
	m := Models{
		Maker: usage.Estimating(fakellm.New("offline-maker",
			fakellm.TextTurn(stubCode), fakellm.TextTurn(task.Buggy), fakellm.TextTurn(task.Reference))),
		Critics: map[Role]model.LLM{
			RoleCorrectness: critic("offline-correctness", script(RoleCorrectness, false, task.Flaw), script(RoleCorrectness, true)),
			RoleStyle:       critic("offline-style", script(RoleStyle, true), script(RoleStyle, true)),
			RolePerformance: critic("offline-performance", script(RolePerformance, true), script(RolePerformance, true)),
		},
		Choices: map[string]modelcfg.Choice{},
	}
	for _, k := range []string{"maker", string(RoleCorrectness), string(RoleStyle), string(RolePerformance)} {
		m.Choices[k] = modelcfg.Choice{Provider: "fakellm", Model: "offline-" + k, Reason: "скрипт, не модель"}
	}
	return m
}

// verdictText серіалізує вердикт так, як його мав би повернути критик.
func verdictText(r Role, passed bool, comments ...string) string {
	q := ""
	for i, c := range comments {
		if i > 0 {
			q += ","
		}
		q += fmt.Sprintf("%q", c)
	}
	return fmt.Sprintf(`{"critic":%q,"passed":%t,"comments":[%s]}`, r, passed, q)
}
