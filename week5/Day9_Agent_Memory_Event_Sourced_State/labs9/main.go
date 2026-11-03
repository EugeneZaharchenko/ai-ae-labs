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
	"path/filepath"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/compaction"

	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
	"github.com/dimetron/ai-eng-course/labs/week5/Day9_Agent_Memory_Event_Sourced_State/labs9/internal/memlab"
)

// user is the support lead whose memory the lab builds; -user changes it for
// one run (the "another user, same database" contrast).
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
	uid := fs.String("user", user, "користувач, від імені якого йде розмова")
	auditDir := fs.String("audit-dir", ".", "куди писати audit_<user>_<session>.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("команда: say <сесія> <текст> | facts <сесія> | audit <сесія> | poison")
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
	app := App{Store: st, Policy: memlab.DefaultPolicy(), Meter: &memlab.Meter{}, User: *uid}

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
	case "audit":
		if len(rest) != 2 {
			return errors.New("audit <сесія>")
		}
		return audit(ctx, out, app, *auditDir, rest[1])
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
	offline := m.Name() == memlab.Offline{}.Name()
	var cfg *compaction.Config
	if threshold > 0 {
		var sum compaction.Summarizer // nil: ADK's LLMSummarizer over the same model
		if offline {
			sum = shortSummarizer{}
		}
		cfg = compactionConfig(threshold, 4, sum)
	}
	r, err := newRunner(ag, app.Store, cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "[%s] > %s\n", sid, text)
	t, err := memlab.Say(ctx, r, app.User, sid, text)
	if err != nil {
		return err
	}
	for _, row := range memlab.Audit(t.Events) {
		fmt.Fprintf(out, "      [ev:%s] %s %s\n", row.Kind, row.Key, row.Detail)
	}
	if t.CompactionErr != nil {
		fmt.Fprintf(out, "      [compaction] помилка: %v\n", t.CompactionErr)
	}
	fmt.Fprintf(out, "[%s] < %s\n", sid, t.Text)
	printUsage(out, app.Meter, offline)
	return nil
}

// printUsage prints one line per model call: the prompt size the provider
// reported and how it splits. Only prompt/output come from UsageMetadata; the
// instruction and facts parts are our EstimateTokens, and "history" is what
// is left (it also holds the tool declarations).
func printUsage(out io.Writer, meter *memlab.Meter, synthetic bool) {
	src := "UsageMetadata"
	if synthetic {
		src = "UsageMetadata offline-моделі (синтетика)"
	}
	instr := memlab.EstimateTokens(Instruction)
	for i, c := range meter.Calls() {
		if !c.ReportedUsage {
			fmt.Fprintf(out, "      [tokens #%d] провайдер не повернув UsageMetadata; факти≈%d (оцінка)\n", i+1, c.FactsTokens)
			continue
		}
		fmt.Fprintf(out, "      [tokens #%d] prompt=%d output=%d (%s) | інструкція≈%d факти≈%d (EstimateTokens, %d/%d фактів) історія+інше≈%d\n",
			i+1, c.PromptTokens, c.OutputTokens, src, instr, c.FactsTokens, c.FactsKept, c.FactsKept+c.FactsDropped,
			int(c.PromptTokens)-instr-c.FactsTokens)
	}
}

// audit writes audit_<user>_<session>.json. The rows come from the stored
// events of the session only — there is no second log to keep in sync.
func audit(ctx context.Context, out io.Writer, app App, dir, sid string) error {
	events, err := memlab.StoredEvents(ctx, app.Store.Sessions, AppName, app.User, sid)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	rows := memlab.Audit(events)
	path := filepath.Join(dir, fmt.Sprintf("audit_%s_%s.json", app.User, sid))
	if err := memlab.WriteAudit(path, rows); err != nil {
		return err
	}
	fmt.Fprintf(out, "журнал: %s (%d подій сесії → %d рядків)\n", path, len(events), len(rows))
	for _, r := range rows {
		fmt.Fprintf(out, "  %s  %-11s %s %s\n", r.At.Local().Format("15:04:05"), r.Kind, r.Key, r.Detail)
	}
	return nil
}

// facts prints the durable user: facts visible from a session.
func facts(ctx context.Context, out io.Writer, app App, sid string) error {
	got, err := app.Store.Sessions.Get(ctx, &session.GetRequest{AppName: AppName, UserID: app.User, SessionID: sid})
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
