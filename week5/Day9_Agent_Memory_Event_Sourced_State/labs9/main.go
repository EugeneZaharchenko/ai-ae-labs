// Стартовий шаблон ДЗ 9 — станом на ADK Go v2.6.0 (жовтень 2026), перевірте актуальність API.
//
// Ранній win (≤15 хв), усе з каталогу labs9:
//
//	go run . say day1 "A-114 сидить на legacy-інтеграції, міграцію відклали до Q4."
//	go run . facts day1            # факти user: і журнал змін стану
//	go run . say day2 "Що порадити мерчанту A-114?"   # інша сесія, інший процес
//	go run . poison                # інцидент: нотатка порталу переписує факт
//
// Ключ можна не експортувати: apps/.env у корені репозиторію підхопиться сам
// (шаблон — apps/.env-example), явний export завжди має пріоритет. Провайдера й
// модель задають DEFAULT_MODEL_PROVIDER і MODEL — той самий вибір, що в лабах
// Тижня 1 (internal/modelcfg). Без провайдера працює правилова offline-модель, і
// про це сказано в першому рядку виводу.
//
// Повний розв'язок із компакцією та OKF — частина вашого завдання (дивіться
// TODO в agent.go).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/compaction"

	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/week5/Day9_Agent_Memory_Event_Sourced_State/labs9/internal/memlab"
)

// user is the support lead whose memory the lab builds.
const user = "iryna"

// resolveModel is a variable so a test can swap the network for a scripted model.
var resolveModel = pickModel

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "помилка:", err)
		os.Exit(1)
	}
}

// pickModel resolves the course model; with no provider it says why and falls
// back to the offline rules.
func pickModel(ctx context.Context, w io.Writer) (model.LLM, string) {
	if err := modelcfg.LoadEnv("."); err != nil {
		fmt.Fprintf(w, "попередження: %v\n", err)
	}
	m, choice, err := modelcfg.Load(ctx)
	if err != nil {
		fmt.Fprintf(w, "провайдера не знайдено (%v) — працює offline-rules; див. Homework, «Якщо щось не працює»\n", err)
		return memlab.Offline{Gullible: true}, "offline-rules (немає провайдера)"
	}
	return m, choice.Reason
}

func run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("labs9", flag.ContinueOnError)
	fs.SetOutput(out)
	dbPath := fs.String("db", memlab.DefaultPath, "SQLite-файл сесій і пам'яті")
	threshold := fs.Int("threshold", 0, "поріг токенів для компакції (tail retention); 0 — вимкнено")
	offline := fs.Bool("offline", false, "не шукати провайдера, використати правилову модель")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("команда: say <сесія> <текст> | facts <сесія> | poison")
	}
	m, note := resolveModel(ctx, out)
	if *offline {
		m, note = memlab.Offline{Gullible: true}, "offline-rules (прапорець -offline)"
	}
	fmt.Fprintf(out, "модель: %s\n", note)

	st, err := memlab.Open(*dbPath, memlab.IngestPolicy{})
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	app := App{Store: st, Policy: memlab.DefaultPolicy(), Meter: &memlab.Meter{}}

	switch rest[0] {
	case "say":
		if len(rest) < 3 {
			return errors.New("say <сесія> <текст>")
		}
		return say(ctx, out, m, app, *threshold, rest[1], strings.Join(rest[2:], " "))
	case "facts":
		if len(rest) != 2 {
			return errors.New("facts <сесія>")
		}
		return facts(ctx, out, app, rest[1])
	case "poison":
		app.Docs = map[string]string{"A-114": InjectedDoc}
		if err := say(ctx, out, m, app, *threshold, "poison", "Подивись, що пишуть у порталі про A-114."); err != nil {
			return err
		}
		return facts(ctx, out, app, "poison")
	default:
		return fmt.Errorf("невідома команда %q", rest[0])
	}
}

// say runs one turn and prints the model answer plus the state changes the
// turn left in the event log.
func say(ctx context.Context, out io.Writer, m model.LLM, app App, threshold int, sid, text string) error {
	ag, err := newAgent(m, app)
	if err != nil {
		return err
	}
	var cfg *compaction.Config
	if threshold > 0 {
		cfg = compactionConfig(threshold, 4, nil)
	}
	r, err := newRunner(ag, app.Store, cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "[%s] > %s\n", sid, text)
	t, err := memlab.Say(ctx, r, user, sid, text)
	if err != nil {
		return err
	}
	for _, row := range memlab.Audit(t.Events) {
		fmt.Fprintf(out, "      [ev:%s] %s %s\n", row.Kind, row.Key, row.Detail)
	}
	fmt.Fprintf(out, "[%s] < %s\n", sid, t.Text)
	return nil
}

// facts prints the durable user: facts visible from a session.
func facts(ctx context.Context, out io.Writer, app App, sid string) error {
	got, err := app.Store.Sessions.Get(ctx, &session.GetRequest{AppName: AppName, UserID: user, SessionID: sid})
	if err != nil {
		return fmt.Errorf("facts: %w", err)
	}
	fs := memlab.FactsFromState(got.Session.State(), app.Policy)
	fmt.Fprintf(out, "      факти user: (%d)\n", len(fs))
	for _, f := range fs {
		fmt.Fprintf(out, "        %s = %s  [%s]\n", f.Key, f.Fact.Value, f.Fact.Tier)
	}
	return nil
}
