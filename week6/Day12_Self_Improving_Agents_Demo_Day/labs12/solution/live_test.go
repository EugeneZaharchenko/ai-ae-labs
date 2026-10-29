package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
)

// liveModelsOrSkip resolves the course provider, or skips with the cause.
// Live tests are opt-in: they spend tokens and need a provider.
func liveModelsOrSkip(t *testing.T) Models {
	t.Helper()
	if os.Getenv("LAB_LIVE") != "1" {
		t.Skip("LAB_LIVE=1 is not set: live tests call a real model and are opt-in")
	}
	if err := modelcfg.LoadEnv("."); err != nil {
		t.Logf("apps/.env: %v", err)
	}
	m := ResolveModels(context.Background(), "strategy.md")
	if !m.Live {
		t.Skipf("no provider resolved by internal/modelcfg: %s", m.Label)
	}
	t.Log(m.Label)
	return m
}

func skipIfProviderRefused(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, marker := range []string{"API key not valid", "API_KEY_INVALID", "invalid_api_key", "insufficient_quota",
		"Forbidden", "401", "403", "429", "failed to verify certificate", "i/o timeout", "connection refused"} {
		if strings.Contains(err.Error(), marker) {
			t.Skipf("the provider call was refused or unreachable (%q): sandbox, proxy, key or quota, not a harness defect: %.300s", marker, err)
		}
	}
}

// A real model proposes, a real model answers, a fixed pure-Go metric decides.
// Whatever words the models use, the invariants of the loop hold: the log
// replays, tokens are accounted from UsageMetadata, the kept strategy still
// parses, and the dev metric never goes down. It does NOT assert that the
// metric went up: a model may legitimately find nothing in two tries, and a
// test that demands improvement would measure luck.
func TestLiveLoopHoldsItsInvariants(t *testing.T) {
	models := liveModelsOrSkip(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	root := t.TempDir()
	cfg := LoopConfig{Root: root, StrategyName: "strategy.md", LogName: "experiments.log",
		Experiments: 2, MaxTokens: 600000, Heldout: true, Models: models}
	var out bytes.Buffer
	sum, err := RunLoop(ctx, cfg, &out)
	skipIfProviderRefused(t, err)
	if err != nil {
		t.Fatalf("RunLoop: %v\n%s", err, out.String())
	}

	if sum.Final < sum.Baseline {
		t.Errorf("dev metric went down: %.3f → %.3f (a discard must restore the baseline)", sum.Baseline, sum.Final)
	}
	if sum.Baseline >= 1 {
		t.Errorf("baseline already scores %.3f: the lab has nothing to improve", sum.Baseline)
	}
	if sum.Tokens <= 0 {
		t.Errorf("tokens = %d: UsageMetadata did not reach the audit trail", sum.Tokens)
	}
	entries := readLog(t, root)
	if len(entries) == 0 {
		t.Fatal("empty audit log")
	}
	if _, err := Replay(entries); err != nil {
		t.Errorf("the log does not replay: %v", err)
	}
	raw, _ := os.ReadFile(root + "/strategy.md")
	if _, err := ParseStrategy(string(raw), []string{"kb_search", "kb_read"}); err != nil {
		t.Errorf("the strategy left on disk is invalid: %v\n%s", err, raw)
	}
	t.Logf("dev %.3f → %.3f, heldout %.3f → %.3f (measured=%v), tokens %d", sum.Baseline, sum.Final,
		sum.HeldoutBefore, sum.HeldoutAfter, sum.HeldoutMeasured, sum.Tokens)
	for _, e := range entries {
		t.Logf("#%d %s %.3f→%.3f eval=%d proposer=%d %s", e.ID, e.Verdict, e.PrevScore, e.NewScore, e.EvalTokens, e.ProposerTokens, e.DiffSummary)
	}
}
