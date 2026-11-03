package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
)

func TestServeLaunchesAgentWithLauncherArgs(t *testing.T) {
	prev := launch
	t.Cleanup(func() { launch = prev })
	var gotName string
	var gotArgs []string
	launch = func(_ context.Context, a agent.Agent, args []string) error {
		gotName, gotArgs = a.Name(), args
		return nil
	}

	var out bytes.Buffer
	err := run(context.Background(), []string{"-offline", "-task", "parse_amount", "web", "webui", "api"}, &out, DefaultResolver())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotName == "" {
		t.Error("агент не передано лаунчеру")
	}
	if strings.Join(gotArgs, " ") != "web webui api" {
		t.Errorf("аргументи лаунчера = %v", gotArgs)
	}
	if !strings.Contains(out.String(), "завдання: parse_amount") {
		t.Errorf("немає назви завдання у виводі: %q", out.String())
	}
}

func TestServeRejectsUnknownTask(t *testing.T) {
	prev := launch
	t.Cleanup(func() { launch = prev })
	launch = func(context.Context, agent.Agent, []string) error {
		t.Fatal("launch не мав викликатися")
		return nil
	}
	err := run(context.Background(), []string{"-offline", "-task", "nope", "console"}, &bytes.Buffer{}, DefaultResolver())
	if err == nil {
		t.Fatal("очікувалась помилка про невідоме завдання")
	}
}
