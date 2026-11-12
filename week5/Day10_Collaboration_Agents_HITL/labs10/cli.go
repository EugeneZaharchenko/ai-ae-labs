package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"slices"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
	"github.com/dimetron/ai-eng-course/labs/week5/Day10_Collaboration_Agents_HITL/labs10/internal/approvals"
)

const appName = "labs10"

const startPrompt = "Підготуй план міграції з legacy-процесора для ради директорів: ризики, комплаєнс PCI DSS / НБУ, мерчанти на legacy-інтеграції."

const usageText = `використання: go run . [прапорці] <команда>
  demo                     start + відповідь у цьому ж процесі (stdin), за замовчуванням
  start                    запустити ран до паузи; рішення — окремим процесом
  pending                  запити, що чекають рішення
  decide <id> <відповідь>  approve | deny | будь-що інше (= invalid); пише в журнал і відновлює ран
  sweep                    прострочені запити → timed_out, ран відновлюється з відмовою
  audit <session>          події рану, журнал рішень і токени`

// resolver picks the model; main passes resolveModel, tests pass an offline one.
type resolver func(context.Context) (model.LLM, string)

type app struct {
	out    io.Writer
	in     io.Reader
	store  *approvals.Store
	live   model.LLM
	label  string
	models func() func(string) model.LLM
	user   string
	by     string
	down   map[string]bool
	window time.Duration
	outbox string
	now    func() time.Time
}

func run(ctx context.Context, args []string, in io.Reader, out io.Writer, resolve resolver) error {
	fs := flag.NewFlagSet("labs10", flag.ContinueOnError)
	fs.SetOutput(out)
	db := fs.String("db", ".data/labs10.db", "SQLite: сесії ADK і журнал рішень")
	sid := fs.String("session", "", "ID сесії для start/demo (за замовчуванням plan-<час>)")
	user := fs.String("user", "iryna", "користувач сесії")
	by := fs.String("by", "oksana", "хто ухвалює рішення")
	window := fs.Duration("window", 2*time.Minute, "вікно рішення; мовчання довше = відмова")
	down := fs.String("down", "", "спеціалісти, чиє джерело мовчить (через кому), напр. integration_desk")
	outbox := fs.String("outbox", ".data/outbox", "куди «надсилається» документ")
	fs.Usage = func() { fmt.Fprintln(out, usageText); fs.PrintDefaults() }
	if err := fs.Parse(args); err != nil {
		return err
	}

	store, err := approvals.Open(*db)
	if err != nil {
		return err
	}
	defer store.Close()

	m, label := resolve(ctx)
	a := &app{
		out: out, in: in, store: store, live: m, label: label,
		user: *user, by: *by, down: parseDown(*down), window: *window, outbox: *outbox, now: time.Now,
	}
	fmt.Fprintln(out, label)

	cmd, rest := "demo", []string(nil)
	if fs.NArg() > 0 {
		cmd, rest = fs.Arg(0), fs.Args()[1:]
	}
	switch cmd {
	case "demo":
		return a.demo(ctx, *sid)
	case "start":
		_, err := a.start(ctx, *sid)
		return err
	case "pending":
		return a.pending(ctx)
	case "decide":
		if len(rest) < 2 {
			return errors.New("decide <id> <approve|deny|…>")
		}
		_, err := a.decide(ctx, rest[0], rest[1])
		return err
	case "sweep":
		_, err := a.sweep(ctx)
		return err
	case "audit":
		if len(rest) < 1 {
			return errors.New("audit <session>")
		}
		return a.audit(ctx, rest[0])
	}
	fs.Usage()
	return fmt.Errorf("невідома команда %q", cmd)
}

func parseDown(s string) map[string]bool {
	out := map[string]bool{}
	for d := range strings.SplitSeq(s, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out[d] = true
		}
	}
	return out
}

// modelFor gives every agent the live model, or its own offline script.
func (a *app) modelFor() func(string) model.LLM {
	if a.models != nil {
		return a.models()
	}
	if a.live != nil {
		return func(string) model.LLM { return a.live }
	}
	return offlineModels(a.down)
}

func (a *app) engine() (agent.Agent, error) {
	return NewEngine(&Engine{
		Store: a.store, Model: a.modelFor(), Down: a.down, Plan: defaultPlan(),
		Recipients: defaultRecipients(), Window: a.window, Outbox: a.outbox,
	})
}

// turn runs one message against the durable session and returns its events.
func (a *app) turn(ctx context.Context, sid string, msg *genai.Content) ([]*session.Event, error) {
	root, err := a.engine()
	if err != nil {
		return nil, err
	}
	r, err := runner.New(runner.Config{
		AppName: appName, Agent: root, SessionService: a.store.Sessions(), AutoCreateSession: true,
	})
	if err != nil {
		return nil, fmt.Errorf("runner: %w", err)
	}
	var events []*session.Event
	for ev, err := range r.Run(ctx, a.user, sid, msg, agent.RunConfig{}) {
		if err != nil {
			return events, fmt.Errorf("ран %s: %w", sid, err)
		}
		events = append(events, ev)
	}
	return events, nil
}

// start runs the team until the gate pauses and returns the interrupt ID.
func (a *app) start(ctx context.Context, sid string) (string, error) {
	if sid == "" {
		sid = "plan-" + a.now().Format("20060102-150405")
	}
	if _, err := a.store.Sessions().Get(ctx, &session.GetRequest{AppName: appName, UserID: a.user, SessionID: sid}); err == nil {
		return "", fmt.Errorf("сесія %s вже існує: start завжди починає нову", sid)
	}
	fmt.Fprintf(a.out, "сесія: %s\n", sid)
	events, err := a.turn(ctx, sid, genai.NewContentFromText(startPrompt, genai.RoleUser))
	printTokens(a.out, events, a.live == nil && a.models == nil)
	if err != nil {
		return "", err
	}
	id := pausedOn(events)
	if id == "" {
		return "", errors.New("ран завершився без паузи перед дією")
	}
	rec, err := a.store.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if err := printRequest(a.out, rec); err != nil {
		return "", err
	}
	fmt.Fprintf(a.out, "ран на паузі. Рішення з іншого процесу: go run . -by oksana decide %s approve|deny\n", id)
	return id, nil
}

func pausedOn(events []*session.Event) string {
	for _, ev := range events {
		if ev.RequestedInput != nil {
			return ev.RequestedInput.InterruptID
		}
	}
	return ""
}

func printRequest(w io.Writer, rec approvals.Record) error {
	ev, err := evidenceOf(rec)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "\nЗАПИТ НА СХВАЛЕННЯ %s (статус %s, вікно до %s)\n", rec.InterruptID, rec.Status, rec.ExpiresAt.Format("15:04:05"))
	fmt.Fprintf(w, "  дія:        %s → %s\n", ev.Action, ev.Artifact)
	fmt.Fprintf(w, "  отримувачі: %s\n", strings.Join(ev.Recipients, ", "))
	fmt.Fprintf(w, "  хеш:        %s\n", ev.Hash)
	fmt.Fprintf(w, "  діф:        %s\n", strings.Join(ev.Diff, "\n              "))
	for _, s := range ev.Sections {
		line := fmt.Sprintf("%s: %s, фактів %d", s.Desk, s.Status, s.Facts)
		if s.Code != "" {
			line += fmt.Sprintf(" — %s: %s", s.Code, s.Reason)
		}
		fmt.Fprintf(w, "  розділ:     %s\n", line)
	}
	for _, f := range ev.RiskFacts {
		fmt.Fprintf(w, "  ризик:      %s\n", f)
	}
	fmt.Fprintf(w, "  джерела:    %s\n", strings.Join(ev.Sources, ", "))
	if len(ev.Gaps) > 0 {
		fmt.Fprintf(w, "  ПРОГАЛИНИ:  %s → approve заблоковано, можливе лише deny\n", strings.Join(ev.Gaps, ", "))
	}
	if len(ev.UnnamedGaps) > 0 {
		fmt.Fprintf(w, "  УВАГА:      підсумок координатора не називає прогалину %s\n", strings.Join(ev.UnnamedGaps, ", "))
	}
	return nil
}

// decide writes the human's answer to the ledger, then resumes the run.
// The resumed gate reads the decision from the ledger, not from this reply.
func (a *app) decide(ctx context.Context, id, word string) (Outcome, error) {
	rec, err := a.store.Get(ctx, id)
	if err != nil {
		return Outcome{}, err
	}
	d := approvals.Decision{By: a.by}
	status, perr := parseVerdict(word)
	switch {
	case perr != nil:
		d.Status, d.Reason = approvals.Invalid, perr.Error()
	case status == approvals.Approved:
		ev, err := evidenceOf(rec)
		if err != nil {
			return Outcome{}, err
		}
		if !ev.ApproveAllowed {
			return Outcome{}, fmt.Errorf("approve заблоковано: %w: %s; можливе лише deny", ErrUnverifiedSection, strings.Join(ev.Gaps, ", "))
		}
		d.Status, d.Reason = approvals.Approved, "людина схвалила"
	default:
		d.Status, d.Reason = approvals.Denied, "людина відмовила"
	}
	if _, err := a.store.Decide(ctx, id, d); err != nil {
		return Outcome{}, err
	}
	fmt.Fprintf(a.out, "журнал: %s → %s (by %s)\n", id, d.Status, d.By)
	return a.resume(ctx, rec, word)
}

// resume sends the reply that wakes the paused gate up.
func (a *app) resume(ctx context.Context, rec approvals.Record, payload string) (Outcome, error) {
	msg := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{
		FunctionResponse: &genai.FunctionResponse{
			ID: rec.InterruptID, Name: workflow.WorkflowInputFunctionCallName,
			Response: map[string]any{"payload": payload},
		},
	}}}
	events, err := a.turn(ctx, rec.SessionID, msg)
	if err != nil {
		return Outcome{}, err
	}
	out, ok := outcomeOf(events)
	if !ok {
		return Outcome{}, fmt.Errorf("ран %s не дійшов до notify_board", rec.SessionID)
	}
	verb := "НЕ ВИКОНАНО"
	if out.Sent {
		verb = "ВИКОНАНО"
	}
	fmt.Fprintf(a.out, "дія %s: %s\n", verb, out.Reason)
	return out, nil
}

func outcomeOf(events []*session.Event) (Outcome, bool) {
	for _, ev := range slices.Backward(events) {
		m, ok := asMap(ev.Output)
		if !ok {
			continue
		}
		if _, ok := m["sent"]; !ok {
			continue
		}
		var out Outcome
		raw, _ := json.Marshal(m)
		if json.Unmarshal(raw, &out) == nil {
			return out, true
		}
	}
	return Outcome{}, false
}

func asMap(v any) (map[string]any, bool) {
	if v == nil {
		return nil, false
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil, false
	}
	return m, true
}

func (a *app) pending(ctx context.Context) error {
	list, err := a.store.PendingList(ctx)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(a.out, "немає запитів, що чекають рішення")
	}
	for _, rec := range list {
		if err := printRequest(a.out, rec); err != nil {
			return err
		}
	}
	return nil
}

// sweep turns overdue requests into timed_out and resumes each run, so it
// ends with a named refusal instead of hanging.
func (a *app) sweep(ctx context.Context) ([]Outcome, error) {
	expired, err := a.store.Expire(ctx)
	if err != nil {
		return nil, err
	}
	if len(expired) == 0 {
		fmt.Fprintln(a.out, "прострочених запитів немає")
	}
	var outs []Outcome
	for _, rec := range expired {
		fmt.Fprintf(a.out, "журнал: %s → %s (%s)\n", rec.InterruptID, rec.Status, rec.Reason)
		out, err := a.resume(ctx, rec, "timeout")
		if err != nil {
			return outs, err
		}
		outs = append(outs, out)
	}
	return outs, nil
}

// demo is start and decide in one process: the reply comes from stdin inside
// the window. The reader goroutine ends when stdin closes; the process owns it.
func (a *app) demo(ctx context.Context, sid string) error {
	id, err := a.start(ctx, sid)
	if err != nil {
		return err
	}
	replies := make(chan string)
	go func() {
		defer close(replies)
		sc := bufio.NewScanner(a.in)
		for sc.Scan() {
			select {
			case replies <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
		// A broken stdin is the same as a closed one: no decision, a named refusal.
		if err := sc.Err(); err != nil {
			log.Printf("stdin: %v", err)
		}
	}()
	rec, err := a.store.Get(ctx, id)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "відповідь (approve/deny) протягом %s:\n", rec.ExpiresAt.Sub(a.now()).Round(time.Second))
	for {
		d, werr := awaitDecision(ctx, replies, rec.ExpiresAt.Sub(a.now()), a.by)
		if werr == nil && d.Status == approvals.Approved {
			ev, err := evidenceOf(rec)
			if err != nil {
				return err
			}
			if !ev.ApproveAllowed {
				fmt.Fprintf(a.out, "approve заблоковано: прогалини %s; можливе лише deny\n", strings.Join(ev.Gaps, ", "))
				continue
			}
		}
		if werr != nil {
			fmt.Fprintf(a.out, "рішення без людини: %v\n", werr)
		}
		if _, err := a.store.Decide(ctx, id, d); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "журнал: %s → %s (by %s: %s)\n", id, d.Status, d.By, d.Reason)
		_, err = a.resume(context.WithoutCancel(ctx), rec, string(d.Status))
		return err
	}
}

func (a *app) audit(ctx context.Context, sid string) error {
	resp, err := a.store.Sessions().Get(ctx, &session.GetRequest{AppName: appName, UserID: a.user, SessionID: sid})
	if err != nil {
		return fmt.Errorf("audit: сесія %s: %w", sid, err)
	}
	var events []*session.Event
	for ev := range resp.Session.Events().All() {
		events = append(events, ev)
	}
	fmt.Fprintf(a.out, "== події сесії %s (%d)\n", sid, len(events))
	printTrace(a.out, events)
	history, err := a.store.History(ctx, sid)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "== журнал рішень (%d)\n", len(history))
	for _, r := range history {
		printLedgerRow(a.out, r)
	}
	printTokens(a.out, events, false)
	return nil
}

func printLedgerRow(w io.Writer, r approvals.Record) {
	decided, consumed := "—", "—"
	if r.DecidedAt != nil {
		decided = r.DecidedAt.Format("15:04:05")
	}
	if r.ConsumedAt != nil {
		consumed = r.ConsumedAt.Format("15:04:05")
	}
	fmt.Fprintf(w, "%s  %s  %s → %s\n  запит %s, вікно до %s, рішення %s by %s, використано %s\n  хеш %s, отримувачі %s\n  причина: %s\n",
		r.InterruptID, r.Action, r.ArtifactURI, r.Status,
		r.RequestedAt.Format("15:04:05"), r.ExpiresAt.Format("15:04:05"), decided, r.DecidedBy, consumed,
		r.Hash, strings.Join(r.RecipientList(), ", "), r.Reason)
}

// offlineModels scripts each agent separately: parallel branches would race
// for one shared script. A down desk's fake smooths the failure over, the way
// a weak model would; the section still becomes a gap.
func offlineModels(down map[string]bool) func(string) model.LLM {
	return func(name string) model.LLM {
		if name == coordinatorName {
			return fakellm.New("offline-coordinator", fakellm.TextTurn(offlineSummary(down)))
		}
		s := desks[name]
		prose := proseOf(s.Fixture)
		if down[name] {
			prose = "Усе гаразд: мерчанти готові до міграції."
		}
		return fakellm.New("offline-"+name,
			fakellm.CallTurn(s.Tool, map[string]any{"topic": "міграція з legacy"}),
			fakellm.TextTurn(prose))
	}
}

func proseOf(fs []Finding) string {
	lines := make([]string, 0, len(fs))
	for _, f := range fs {
		lines = append(lines, fmt.Sprintf("%s (%s)", f.Claim, f.Source))
	}
	return strings.Join(lines, "\n")
}

func offlineSummary(down map[string]bool) string {
	var sb strings.Builder
	sb.WriteString("Міграцію можна планувати з умовами: попередній cutover відкотили за 41 хв (incident-db:INC-418); " +
		"PAN треба токенізувати до перенесення (pci-dss:req-3.5.1); НБУ повідомляємо за 30 днів (nbu:payments-notice-30d).")
	if !down[integrationDesk] {
		sb.WriteString(" 37 мерчантів ще на legacy API v1 (crm:legacy-merchants-2026-10).")
	}
	for _, name := range defaultPlan().Desks {
		if down[name] {
			s := desks[name]
			fmt.Fprintf(&sb, "\nПРОГАЛИНА: %s — %s: джерело %s не відповіло", name, s.ErrCode, s.Source)
		}
	}
	return sb.String()
}
