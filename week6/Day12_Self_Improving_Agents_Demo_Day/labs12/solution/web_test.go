package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
)

func TestIsServeCmd(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		args []string
		want bool
	}{
		"web":     {[]string{"web", "webui"}, true},
		"console": {[]string{"console"}, true},
		"flag":    {[]string{"--report-only"}, false},
		"empty":   {nil, false},
	}
	for name, c := range cases {
		if got := isServeCmd(c.args); got != c.want {
			t.Errorf("%s: isServeCmd(%v) = %v, want %v", name, c.args, got, c.want)
		}
	}
}

// Не parallel: тест підміняє пакетні шви й змінну оточення.
func TestServeSeedsStrategyAndLaunchesWorker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "work")
	t.Setenv("LAB12_ROOT", root)

	prevModels, prevLaunch := resolveModels, launch
	t.Cleanup(func() { resolveModels, launch = prevModels, prevLaunch })
	resolveModels = func(context.Context, string) Models {
		return Models{Worker: fakellm.New("w"), Label: "test"}
	}
	var gotName string
	var gotArgs []string
	launch = func(_ context.Context, a agent.Agent, args []string) error {
		gotName, gotArgs = a.Name(), args
		return nil
	}

	var out bytes.Buffer
	if err := run([]string{"web", "webui", "api"}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotName != "ledger_research_agent" {
		t.Errorf("agent = %q", gotName)
	}
	if strings.Join(gotArgs, " ") != "web webui api" {
		t.Errorf("launcher args = %v", gotArgs)
	}
	if _, err := os.Stat(filepath.Join(root, "strategy.md")); err != nil {
		t.Errorf("strategy.md не створено: %v", err)
	}
	if !strings.Contains(out.String(), "створено базовий strategy.md") {
		t.Errorf("немає повідомлення про створення: %q", out.String())
	}
}

func TestServeRejectsInvalidStrategy(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAB12_ROOT", root)
	if err := os.WriteFile(filepath.Join(root, "strategy.md"), []byte("not a strategy"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := launch
	t.Cleanup(func() { launch = prev })
	launch = func(context.Context, agent.Agent, []string) error {
		t.Fatal("launch не мав викликатися")
		return nil
	}
	if err := run([]string{"console"}, &bytes.Buffer{}); err == nil {
		t.Fatal("очікувалась помилка валідації strategy.md")
	}
}
