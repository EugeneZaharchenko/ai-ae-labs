package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"google.golang.org/adk/v2/agent"

	"github.com/dimetron/ai-eng-course/labs/internal/labrun"
	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/ledger"
	"github.com/dimetron/ai-eng-course/labs/week4/Day7_ReAct_Loop_Internals/labs7/internal/react"
)

// Контрактні тести. Приберіть t.Skip, коли реалізуєте Run у react.go.

// Мова завдання — довільний текст, тож «web» у ній не рідкість. Розрізняти
// підкоманду лаунчера й завдання треба до рану: помилка тут означає або сервер,
// якого ніхто не просив, або одноразовий ран із завданням «web webui api».
func TestLauncherArgsTellsASubcommandFromATask(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "web with its sublaunchers", args: []string{"web", "webui", "api"}, want: true},
		{name: "console", args: []string{"console"}, want: true},
		{name: "web anywhere in the launcher flags", args: []string{"-host", "127.0.0.1", "web"}, want: true},
		{name: "no args at all", args: nil, want: false},
		{name: "a task, not a subcommand", args: []string{"Investigate the payout incident for A-114"}, want: false},
		{name: "a task that merely mentions the web", args: []string{"check the web runbook"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := launcherArgs(tt.args); got != tt.want {
				t.Errorf("launcherArgs(%q) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// `:team:web` працює, лише якщо до лаунчера пішов САМЕ граф із трьох llmagent, з
// підкомандами `web webui api` і тим самим завданням, що й в одноразовому рані.
// Справжній лаунчер тут не запустити (він блокує), тож підмінюємо його й
// перевіряємо те, що він дістав.
func TestRunTeamLauncherHandsTheGraphToTheLauncher(t *testing.T) {
	// Провайдера немає — вузли беруть скриптовані моделі, і ланцюг не залежить
	// від мережі. `t.Chdir` обов'язковий: modelcfg.LoadEnv(".") шукає apps/.env
	// угору по дереву, і без нього тест знайшов би справжній ключ з apps/.env і
	// пішов би в мережу за відповіддю моделі.
	offlineEnv(t)

	orig := runLauncher
	t.Cleanup(func() { runLauncher = orig })

	var got agent.Agent
	var gotArgs []string
	runLauncher = func(_ context.Context, a agent.Agent, args []string) error {
		got, gotArgs = a, args
		return nil
	}

	if err := runTeamLauncher(context.Background(), []string{"web", "webui", "api"}); err != nil {
		t.Fatalf("runTeamLauncher: %v", err)
	}
	if got == nil {
		t.Fatal("launcher got a nil agent")
	}
	if name := got.Name(); name != "ledgerworks_team" {
		t.Errorf("agent = %q, want the graph ledgerworks_team, not the single llmagent", name)
	}
	if want := []string{"web", "webui", "api"}; !slices.Equal(gotArgs, want) {
		t.Errorf("args = %q, want %q", gotArgs, want)
	}

	// Той самий граф мусить реально відпрацювати: три вузли, один тікет.
	res, err := labrun.Run(context.Background(), got, defaultTask)
	if err != nil {
		t.Fatalf("run the graph handed to the launcher: %v", err)
	}
	for _, name := range []string{"search_internal_wiki", "get_merchant_payouts", "create_jira_ticket"} {
		if !res.CalledTool(name) {
			t.Errorf("tool %s never called; the graph in the browser would show nothing: %v", name, res.ToolCalls)
		}
	}
}

// offlineEnv прибирає з оточення все, з чого modelcfg вибирає провайдера, і
// відводить робочий каталог туди, де apps/.env немає.
//
// Одного t.Setenv мало: `adkenv.Key` вважає порожнє значення незаданим, але
// `LoadEnv(".")` шукає apps/.env угору по дереву й ДОЗАПИСУЄ звідти будь-який
// ключ, якого немає в оточенні. Тому обидва кроки обов'язкові — інакше тест
// бере справжній ключ розробника й іде в мережу.
func offlineEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"MODEL", "DEFAULT_MODEL_PROVIDER",
		"AGENTGATEWAY_BASE_URL", "AGENTGATEWAY_MODEL", "AGENTGATEWAY_API_KEY",
		"OLLAMA_BASE_URL", "OLLAMA_MODEL", "OLLAMA_API_KEY",
		"GOOGLE_API_KEY", "GEMINI_API_KEY", "GEMINI_MODEL",
		"OPENAI_API_KEY", "OPENAI_MODEL",
	} {
		t.Setenv(name, "")
	}
	t.Chdir(t.TempDir()) // жодного .env поруч
}

// endless — планувальник, що ніколи не каже «готово».
type endless struct{ n int }

func (e *endless) NextStep(context.Context, react.History) (react.Step, error) {
	e.n++
	return react.Step{Thought: "ще раз", Calls: []react.ToolCall{{
		ID: "c" + string(rune('0'+e.n%10)), Name: "search_internal_wiki",
		Args: map[string]any{"query": e.n}}}}, nil
}

func TestRunStopsAtIterationLimit(t *testing.T) {
	t.Skip("прибрати, коли Run реалізовано")
	_, _, err := Run(context.Background(), &endless{}, ledger.New().Tools(), "task", react.Budget{MaxIterations: 5})
	if !errors.Is(err, ErrMaxIterations) {
		t.Fatalf("err = %v; want errors.Is(err, ErrMaxIterations)", err)
	}
}

func TestRunKeepsObservationsInCallOrder(t *testing.T) {
	t.Skip("прибрати, коли Run реалізовано")
	_, tr, err := Run(context.Background(), &react.RulePlanner{}, ledger.New().Tools(), "task", react.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	for i, step := range tr.Steps[:len(tr.Observations)] {
		for j, c := range step.Calls {
			if got := tr.Observations[i][j].CallID; got != c.ID {
				t.Fatalf("step %d: observation %d has CallID %q, want %q", i+1, j, got, c.ID)
			}
		}
	}
}
