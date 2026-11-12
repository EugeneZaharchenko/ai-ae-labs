package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/billing"
)

// These tests run the real graph and the real gate (go build/vet/test),
// only the models are scripted. No API key, no network.

func mustTask(t *testing.T, id string) billing.Task {
	t.Helper()
	task, ok := billing.ByID(id)
	if !ok {
		t.Fatalf("немає задачі %q", id)
	}
	return task
}

func hasKey(r taskResult, key string) bool {
	return strings.Contains(eventLog(r.Events), key+" =")
}

func TestLoop_ConvergesOnThirdAttempt(t *testing.T) {
	t.Parallel()
	for _, parallel := range []bool{true, false} {
		name := "parallel"
		if !parallel {
			name = "sequential"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			task := mustTask(t, "prorate_refund")
			r := runTask(context.Background(), task, offlineModels(task), runOptions{maxAttempts: 4, parallel: parallel})
			if !r.Accepted || r.Err != "" {
				t.Fatalf("accepted=%t err=%s\n%s", r.Accepted, r.Err, eventLog(r.Events))
			}
			if r.Attempts != 3 || r.CriticRounds != 2 {
				t.Fatalf("attempts=%d rounds=%d, хотіли 3 і 2", r.Attempts, r.CriticRounds)
			}
			if !r.HoldoutOK || r.FalseAccept() {
				t.Fatalf("holdout=%t stage=%s", r.HoldoutOK, r.HoldoutStage)
			}
			for _, k := range []string{"w6p1:attempt:1", "w6p1:attempt:2", "w6p1:attempt:3", keyDone} {
				if !hasKey(r, k) {
					t.Errorf("в event log немає %s", k)
				}
			}
			if r.Tokens.Total() == 0 {
				t.Error("токени не пораховані")
			}
		})
	}
}

func TestLoop_BudgetExhausted(t *testing.T) {
	t.Parallel()
	task := mustTask(t, "validate_iban")
	stub := fakellm.TextTurn("package billing\n\n// TODO\n")
	m := models{
		maker: fakellm.New("stub-maker", stub, stub),
		// critics have no turns: if the loop calls them on broken code, the script fails
		critics: map[string]model.LLM{
			RoleCorrectness: fakellm.New("c"), RoleStyle: fakellm.New("s"), RoleSecurity: fakellm.New("sec"),
		},
	}
	r := runTask(context.Background(), task, m, runOptions{maxAttempts: 2, parallel: true})
	if r.Accepted {
		t.Fatal("заглушку не можна приймати")
	}
	if !strings.Contains(r.Err, "budget exhausted") {
		t.Fatalf("err = %q, хотіли budget exhausted", r.Err)
	}
	if !hasKey(r, keyExhausted) || hasKey(r, keyDone) {
		t.Fatalf("event log:\n%s", eventLog(r.Events))
	}
	if r.Attempts != 2 || r.CriticRounds != 0 {
		t.Fatalf("attempts=%d rounds=%d, хотіли 2 і 0", r.Attempts, r.CriticRounds)
	}
	t.Log("\n" + eventLog(r.Events))
}

// A broken critic answer must not be "interpreted": it is marked in the event
// log and counts as passed=false, so the loop goes for another attempt.
func TestLoop_InvalidVerdictIsRejected(t *testing.T) {
	t.Parallel()
	bad := map[string]fakellm.Turn{
		"проза":         fakellm.TextTurn("Виглядає добре, мержимо!"),
		"підміна ролі":  fakellm.TextTurn(`{"critic":"style","passed":true}`),
		"невідома роль": fakellm.TextTurn(`{"critic":"admin","passed":true}`),
		"без коментаря": fakellm.TextTurn(`{"critic":"correctness","passed":false}`),
		// a model error (timeout, network) must not kill the loop
		"критик впав": {Err: errors.New("context deadline exceeded")},
	}
	for name, answer := range bad {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			task := mustTask(t, "validate_iban")
			ref := fakellm.TextTurn(task.Reference)
			ok := func(role string) fakellm.Turn { return fakellm.TextTurn(scriptedVerdict(role, true)) }
			m := models{
				maker: fakellm.New("maker", ref, ref),
				critics: map[string]model.LLM{
					RoleCorrectness: fakellm.New("c", answer, ok(RoleCorrectness)),
					RoleStyle:       fakellm.New("s", ok(RoleStyle), ok(RoleStyle)),
					RoleSecurity:    fakellm.New("sec", ok(RoleSecurity), ok(RoleSecurity)),
				},
			}
			r := runTask(context.Background(), task, m, runOptions{maxAttempts: 3, parallel: true})
			if !r.Accepted || r.Attempts != 2 {
				t.Fatalf("accepted=%t attempts=%d, хотіли прийняття на 2-й спробі\n%s", r.Accepted, r.Attempts, eventLog(r.Events))
			}
			log := eventLog(r.Events)
			first := log[:strings.Index(log, "w6p1:attempt:2")]
			if !strings.Contains(first, "rejected:[correctness: невалідний вердикт") || !strings.Contains(first, "decision:retry") {
				t.Fatalf("спроба 1 мала бути відхилена з позначкою:\n%s", first)
			}
		})
	}
}

func TestLoopTimeoutCoversAllNodes(t *testing.T) {
	t.Parallel()
	perAttempt := makerTimeout + gateTimeout + criticTimeout
	if loopTimeout(maxAttempts) <= perAttempt*maxAttempts {
		t.Fatalf("loopTimeout %v має бути більший за %v", loopTimeout(maxAttempts), perAttempt*maxAttempts)
	}
	if gateTimeout <= 3*goCallTimeout {
		t.Fatalf("gateTimeout %v має бути більший за 3 виклики go по %v", gateTimeout, goCallTimeout)
	}
}
