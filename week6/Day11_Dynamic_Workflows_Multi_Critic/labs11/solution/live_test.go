package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/billing"
	"github.com/dimetron/ai-eng-course/labs/week6/Day11_Dynamic_Workflows_Multi_Critic/labs11/internal/gate"
)

// TestLive_RealModelLoop — opt-in: LAB_LIVE=1 і провайдер із apps/.env або
// Ollama. Перевіряє ІНВАРІАНТИ, а не точні рядки: модель недетермінована.
//
//   - цикл завершується в межах бюджету (прийняв або чесно вичерпав спроби);
//   - якщо прийняв — код справді зібрався (holdout-стадія не "build");
//   - токени лічаться з UsageMetadata провайдера (не нуль для живої моделі).
//
// «Прийнято, але хибне на holdout» тут НЕ помилка тесту: це і є вимірюваний
// eval-сигнал, і саме тому він друкується, а не ховається.
func TestLive_RealModelLoop(t *testing.T) {
	if testing.Short() || os.Getenv("LAB_LIVE") != "1" {
		t.Skip("живий тест: задайте LAB_LIVE=1 (і провайдера в apps/.env або Ollama)")
	}
	_ = modelcfg.LoadEnv(".")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	models, err := DefaultResolver().Resolve(ctx)
	if err != nil {
		t.Skipf("живий тест пропущено: провайдер не визначено: %v", err)
	}
	t.Logf("моделі:\n%s", models.Describe())

	task, _ := billing.ByID("validate_iban")
	r := RunTask(ctx, task, models, Options{MaxAttempts: 4, Gate: gate.Runner{}})
	t.Logf("outcome: %+v", r.Outcome)

	if r.Outcome.Attempts < 1 || r.Outcome.Attempts > 4 {
		t.Errorf("спроб поза бюджетом: %d", r.Outcome.Attempts)
	}
	if r.Outcome.Tokens.Total() == 0 {
		t.Error("токени з UsageMetadata не пораховано")
	}
	if r.Outcome.Accepted && r.Outcome.HoldoutStage == "build" {
		t.Error("прийнятий код не збирається — gate пропустив неможливе")
	}
	if !r.Outcome.Accepted && r.Outcome.Err == "" {
		t.Error("відмова без причини: Err порожній")
	}
}
