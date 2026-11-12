package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/dimetron/ai-eng-course/labs/week5/Day10_Collaboration_Agents_HITL/labs10/internal/approvals"
)

const coordinatorName = "coordinator"

// Plan is what the coordinator hands to the team. Desks is also the order of
// the sections in the document.
type Plan struct {
	Topic    string   `json:"topic"`
	Desks    []string `json:"desks"`
	Deadline string   `json:"deadline"`
}

// Section is one desk's part of the document.
type Section struct {
	Desk     string    `json:"desk"`
	Title    string    `json:"title"`
	Verified bool      `json:"verified"`
	Findings []Finding `json:"findings,omitempty"`
	Code     string    `json:"code,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	// Prose is what the model said. It is kept for the audit only.
	Prose string `json:"prose,omitempty"`
}

// Brief is the merged input for the coordinator.
type Brief struct {
	Topic    string    `json:"topic"`
	Sections []Section `json:"sections"`
}

// Gaps lists the sections that are not verified.
func (b Brief) Gaps() []Section {
	var out []Section
	for _, s := range b.Sections {
		if !s.Verified {
			out = append(out, s)
		}
	}
	return out
}

// Draft is the document the human is asked to approve.
type Draft struct {
	URI        string    `json:"uri"`
	Hash       string    `json:"hash"`
	Document   string    `json:"document"`
	Diff       []string  `json:"diff"`
	Summary    string    `json:"summary"`
	Sections   []Section `json:"sections"`
	Recipients []string  `json:"recipients"`
	// UnnamedGaps are gaps the coordinator's summary does not mention.
	UnnamedGaps []string `json:"unnamed_gaps,omitempty"`
}

// GateResult is the decision read back from the ledger.
type GateResult struct {
	InterruptID string           `json:"interrupt_id"`
	Status      approvals.Status `json:"status"`
	By          string           `json:"by"`
	Reason      string           `json:"reason"`
	Draft       Draft            `json:"draft"`
}

// Outcome is the end of the run: sent or not, and why.
type Outcome struct {
	Sent        bool             `json:"sent"`
	InterruptID string           `json:"interrupt_id"`
	Status      approvals.Status `json:"status"`
	Reason      string           `json:"reason"`
	Path        string           `json:"path,omitempty"`
}

// ErrUnverifiedSection — the document has a gap, so it cannot be approved or sent.
var ErrUnverifiedSection = errors.New("документ має неперевірений розділ")

// Engine is the team and its wiring.
type Engine struct {
	Store *approvals.Store
	// Model returns the model for one agent; offline each agent gets its own script.
	Model      func(agent string) model.LLM
	Down       map[string]bool
	Plan       Plan
	Recipients []string
	Window     time.Duration
	Outbox     string
}

func defaultPlan() Plan {
	return Plan{
		Topic:    "План міграції з legacy-процесора для ради директорів",
		Desks:    []string{riskDesk, complianceDesk, integrationDesk},
		Deadline: "понеділок 18:00",
	}
}

func defaultRecipients() []string {
	return []string{"board@ledgerworks.example", "iryna@ledgerworks.example"}
}

const coordinatorInstruction = `Ти — координатор команди LEDGERWORKS. На вході JSON з розділами від спеціалістів.
1. Напиши підсумок для ради директорів: 3–5 речень.
2. Використовуй ЛИШЕ факти з розділів, де "verified": true. Біля кожного факту — джерело в дужках.
3. Для КОЖНОГО розділу з "verified": false напиши окремий рядок:
   ПРОГАЛИНА: <desk> — <code>: <reason>
   Не вгадуй, що було б у цьому розділі. Не пиши, що все гаразд.
4. Не додавай фактів, яких немає у вхідних даних.`

// NewEngine builds the graph:
//
//	START → plan ─┬─> risk_desk ───────┐
//	              ├─> compliance_desk ─┼─> gather (Join) → merge → coordinator → draft → approval_gate → notify_board
//	              └─> integration_desk ┘
func NewEngine(e *Engine) (agent.Agent, error) {
	if e.Store == nil || e.Model == nil {
		return nil, errors.New("engine: Store і Model обов'язкові")
	}
	var deskNodes []workflow.Node
	var subAgents []agent.Agent
	for _, name := range e.Plan.Desks {
		spec, ok := desks[name]
		if !ok {
			return nil, fmt.Errorf("engine: невідомий спеціаліст %q у плані", name)
		}
		a, err := newSpecialist(spec, e.Model(name), e.Down[name])
		if err != nil {
			return nil, err
		}
		n, err := workflow.NewAgentNode(a, workflow.NodeConfig{})
		if err != nil {
			return nil, fmt.Errorf("engine: node %s: %w", name, err)
		}
		deskNodes = append(deskNodes, n)
		subAgents = append(subAgents, a)
	}

	coord, err := llmagent.New(llmagent.Config{
		Name:        coordinatorName,
		Model:       e.Model(coordinatorName),
		Description: "Зводить розділи спеціалістів у підсумок і називає прогалини.",
		Instruction: coordinatorInstruction,
	})
	if err != nil {
		return nil, fmt.Errorf("engine: coordinator: %w", err)
	}
	coordNode, err := workflow.NewAgentNode(coord, workflow.NodeConfig{})
	if err != nil {
		return nil, fmt.Errorf("engine: coordinator node: %w", err)
	}
	subAgents = append(subAgents, coord)

	plan := workflow.NewFunctionNode("plan", func(agent.Context, any) (Plan, error) {
		return e.Plan, nil
	}, workflow.NodeConfig{})
	gather := workflow.NewJoinNode("gather")
	merge := workflow.NewFunctionNode("merge", e.Merge, workflow.NodeConfig{})
	draft := workflow.NewFunctionNode("draft", e.draft, workflow.NodeConfig{})
	gate := newApprovalGateNode(e)
	notify := workflow.NewFunctionNode("notify_board", e.notifyBoard, workflow.NodeConfig{})

	eb := workflow.NewEdgeBuilder()
	eb.Add(workflow.Start, plan)
	eb.AddFanOut(plan, deskNodes...)
	eb.AddFanIn(gather, deskNodes...)
	eb.Add(gather, merge)
	eb.Add(merge, coordNode)
	eb.Add(coordNode, draft)
	eb.Add(draft, gate)
	eb.Add(gate, notify)

	return workflowagent.New(workflowagent.Config{
		Name:        "ledgerworks_team",
		Description: "Координатор, три спеціалісти й затвор схвалення.",
		Edges:       eb.Build(),
		SubAgents:   subAgents,
	})
}

// Merge turns the join map into sections, in plan order. The facts come from
// the recorded tool outcomes; the join map only gives the model's prose.
func (e *Engine) Merge(ctx agent.Context, gathered map[string]any) (Brief, error) {
	b := Brief{Topic: e.Plan.Topic}
	for _, name := range e.Plan.Desks {
		s := sectionFromState(ctx.State(), name)
		if v, ok := gathered[name]; ok && v != nil {
			s.Prose = strings.TrimSpace(fmt.Sprint(v))
		}
		b.Sections = append(b.Sections, s)
	}
	return b, nil
}

// sectionFromState decides if a section is verified. Every path that is not
// "the tool answered with sourced facts" becomes a named gap.
func sectionFromState(st session.ReadonlyState, desk string) Section {
	s := Section{Desk: desk, Title: desks[desk].Title}
	v, err := st.Get(outcomeKey(desk))
	if err != nil {
		s.Code, s.Reason = "NO_TOOL_CALL", "спеціаліст відповів без виклику інструмента"
		return s
	}
	raw, _ := v.(string)
	var out toolOutcome
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		s.Code, s.Reason = "BAD_OUTCOME", "запис інструмента не читається: "+err.Error()
		return s
	}
	switch {
	case !out.OK:
		s.Code, s.Reason = out.Code, out.Error
	case len(out.Findings) == 0:
		s.Code, s.Reason = "NO_FINDINGS", "інструмент не повернув жодного факту"
	default:
		for _, f := range out.Findings {
			if strings.TrimSpace(f.Source) == "" {
				s.Code, s.Reason = "UNSOURCED", "факт без джерела: "+f.Claim
				return s
			}
		}
		s.Verified, s.Findings = true, out.Findings
	}
	return s
}

// draft renders the document the human will approve. It reads the sections
// from state again: the coordinator node only passes its own text along.
func (e *Engine) draft(ctx agent.Context, summary string) (Draft, error) {
	b, err := e.Merge(ctx, nil)
	if err != nil {
		return Draft{}, err
	}
	summary = strings.TrimSpace(summary)
	doc := renderDocument(e.Plan, summary, b.Sections)
	prev, _ := os.ReadFile(e.outboxFile())
	hash := hashOf(doc)
	// The reviewer reads the exact text the hash pins; the name makes the write idempotent.
	path := filepath.Join(e.Outbox, "drafts", strings.TrimPrefix(hash, "sha256:")[:16]+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Draft{}, fmt.Errorf("draft: %w", err)
	}
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		return Draft{}, fmt.Errorf("draft: %w", err)
	}
	d := Draft{
		URI:        path,
		Hash:       hash,
		Document:   doc,
		Diff:       lineDiff(string(prev), doc),
		Summary:    summary,
		Sections:   b.Sections,
		Recipients: slices.Clone(e.Recipients),
	}
	for _, g := range b.Gaps() {
		if !strings.Contains(summary, g.Desk) {
			d.UnnamedGaps = append(d.UnnamedGaps, g.Desk)
		}
	}
	return d, nil
}

func renderDocument(p Plan, summary string, sections []Section) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\nТермін: %s. Підпис: Оксана.\n\n## Підсумок координатора\n\n%s\n", p.Topic, p.Deadline, summary)
	for _, s := range sections {
		fmt.Fprintf(&sb, "\n## %s (%s)\n\n", s.Title, s.Desk)
		if !s.Verified {
			fmt.Fprintf(&sb, "ПРОГАЛИНА (%s): %s. Розділ не заповнено.\n", s.Code, s.Reason)
			continue
		}
		for _, f := range s.Findings {
			fmt.Fprintf(&sb, "- %s (%s)\n", f.Claim, f.Source)
		}
	}
	return sb.String()
}

func hashOf(doc string) string {
	sum := sha256.Sum256([]byte(doc))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// lineDiff is a small set diff: lines that left and lines that came in.
// Enough for a reviewer to see what changed since the last sent version.
func lineDiff(prev, next string) []string {
	if prev == "" {
		return []string{"(перша версія: попередньої відправленої немає)"}
	}
	before := strings.Split(prev, "\n")
	after := strings.Split(next, "\n")
	var out []string
	for _, l := range before {
		if l != "" && !slices.Contains(after, l) {
			out = append(out, "- "+l)
		}
	}
	for _, l := range after {
		if l != "" && !slices.Contains(before, l) {
			out = append(out, "+ "+l)
		}
	}
	if len(out) == 0 {
		return []string{"(без змін відносно відправленої версії)"}
	}
	return out
}

func (e *Engine) outboxFile() string { return filepath.Join(e.Outbox, "board-pack.md") }

// notifyBoard is the risky action. It runs only with a matching, unspent
// approval from the ledger, and never for a document with a gap.
func (e *Engine) notifyBoard(ctx agent.Context, g GateResult) (Outcome, error) {
	out := Outcome{InterruptID: g.InterruptID, Status: g.Status}
	if g.Status != approvals.Approved {
		out.Reason = fmt.Sprintf("дію не виконано: %s (%s)", g.Status, g.Reason)
		return out, nil
	}
	if gaps := (Brief{Sections: g.Draft.Sections}).Gaps(); len(gaps) > 0 {
		out.Reason = fmt.Sprintf("дію не виконано: %v: %s", ErrUnverifiedSection, gaps[0].Desk)
		return out, nil
	}
	// The hash is recomputed from the text, so a changed document cannot use an old approval.
	if err := e.Store.Spend(ctx, g.InterruptID, hashOf(g.Draft.Document), g.Draft.Recipients); err != nil {
		out.Reason = "дію не виконано: " + err.Error()
		return out, nil
	}
	if err := os.MkdirAll(e.Outbox, 0o755); err != nil {
		return e.unspend(ctx, out, err)
	}
	if err := os.WriteFile(e.outboxFile(), []byte(g.Draft.Document), 0o644); err != nil {
		return e.unspend(ctx, out, err)
	}
	out.Sent, out.Path = true, e.outboxFile()
	out.Reason = fmt.Sprintf("надіслано %v, схвалив %s", g.Draft.Recipients, g.By)
	return out, nil
}

func (e *Engine) unspend(ctx agent.Context, out Outcome, cause error) (Outcome, error) {
	if err := e.Store.Unspend(ctx, out.InterruptID, "відправка впала: "+cause.Error()); err != nil {
		return out, fmt.Errorf("notify_board: %w (unspend: %w)", cause, err)
	}
	return out, fmt.Errorf("notify_board: %w", cause)
}
