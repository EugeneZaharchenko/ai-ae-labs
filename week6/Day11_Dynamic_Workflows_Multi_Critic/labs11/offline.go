package main

import (
	"fmt"

	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/billing"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/usage"
)

// offlineModels — scripted models for runs without an API key.
// The gate and the graph are real, only the model answers are scripted:
//
//	attempt 1: stub       → compiler fails, LLM critics are not called
//	attempt 2: Buggy      → green on visible tests, correctness says passed=false
//	attempt 3: Reference  → all critics pass
//
// usage.Estimating gives rough token numbers, so the budget code is exercised.
func offlineModels(task billing.Task) models {
	critic := func(role string, firstPassed bool, firstComment string) model.LLM {
		var first string
		if firstPassed {
			first = scriptedVerdict(role, true)
		} else {
			first = scriptedVerdict(role, false, firstComment)
		}
		return usage.Estimating(fakellm.New("offline-"+role,
			fakellm.TextTurn(first), fakellm.TextTurn(scriptedVerdict(role, true))))
	}
	return models{
		maker: usage.Estimating(fakellm.New("offline-maker",
			fakellm.TextTurn("package billing\n\n// TODO\n"),
			fakellm.TextTurn(task.Buggy),
			fakellm.TextTurn(task.Reference))),
		critics: map[string]model.LLM{
			RoleCorrectness: critic(RoleCorrectness, false, task.Flaw),
			RoleStyle:       critic(RoleStyle, true, ""),
			RoleSecurity:    critic(RoleSecurity, true, ""),
		},
	}
}

func scriptedVerdict(role string, passed bool, comments ...string) string {
	list := ""
	for i, c := range comments {
		if i > 0 {
			list += ","
		}
		list += fmt.Sprintf("%q", c)
	}
	return fmt.Sprintf(`{"critic":%q,"passed":%t,"comments":[%s]}`, role, passed, list)
}
