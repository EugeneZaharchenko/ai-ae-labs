package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// Desk names double as agent names and JoinNode keys.
const (
	riskDesk        = "risk_desk"
	complianceDesk  = "compliance_desk"
	integrationDesk = "integration_desk"
)

// Finding — один факт із ідентифікатором джерела. Спеціаліст має право
// говорити лише те, що повернув інструмент.
type Finding struct {
	Claim  string `json:"claim"`
	Source string `json:"source"`
}

type deskInput struct {
	Topic string `json:"topic" jsonschema:"Тема, за якою потрібні факти"`
}

type deskOutput struct {
	Findings []Finding `json:"findings"`
}

// SourceError is a source that did not answer. Code goes into the evidence
// as is, so the reviewer sees INTEGRATION_TIMEOUT and not a paragraph.
type SourceError struct {
	Code   string
	Source string
}

func (e *SourceError) Error() string {
	return fmt.Sprintf("%s: джерело %s не відповіло", e.Code, e.Source)
}

// deskSpec is one specialist: its tool, its fixture and its instruction.
type deskSpec struct {
	Name        string
	Title       string
	Tool        string
	ToolDesc    string
	Source      string
	ErrCode     string
	Fixture     []Finding
	Description string
}

var desks = map[string]deskSpec{
	riskDesk: {
		Name: riskDesk, Title: "Ризики міграції",
		Tool: "lookup_risk_desk", ToolDesc: "Факти про ризики міграції з бази інцидентів і ранбуків.",
		Source: "incident-db", ErrCode: "INCIDENT_DB_TIMEOUT",
		Description: "Спеціаліст з ризиків міграції.",
		Fixture: []Finding{
			{Claim: "Cutover 2025-03 відкотили за 41 хв через розсинхрон токенів", Source: "incident-db:INC-418"},
			{Claim: "Rollback-ранбук для A-114 перевірено востаннє у вересні", Source: "runbook:cutover-rollback"},
			{Claim: "Пік legacy-процесора — 1840 TPS у п'ятницю ввечері", Source: "capacity:legacy-q3"},
		},
	},
	complianceDesk: {
		Name: complianceDesk, Title: "Комплаєнс PCI DSS / НБУ",
		Tool: "lookup_compliance_desk", ToolDesc: "Вимоги PCI DSS і НБУ до зміни процесингу.",
		Source: "compliance-register", ErrCode: "COMPLIANCE_REGISTER_TIMEOUT",
		Description: "Спеціаліст з комплаєнсу PCI DSS і НБУ.",
		Fixture: []Finding{
			{Claim: "PCI DSS 4.0.1: PAN має бути токенізовано до перенесення даних карт", Source: "pci-dss:req-3.5.1"},
			{Claim: "Про зміну процесингового центру НБУ повідомляють за 30 днів", Source: "nbu:payments-notice-30d"},
			{Claim: "Останній QSA-аудит 2026-06-14 пройдено без зауважень", Source: "audit:qsa-2026-06"},
		},
	},
	integrationDesk: {
		Name: integrationDesk, Title: "Мерчанти на legacy-інтеграції",
		Tool: "lookup_integration_desk", ToolDesc: "Мерчанти, які досі працюють через legacy API.",
		Source: "integration-crm", ErrCode: "INTEGRATION_TIMEOUT",
		Description: "Спеціаліст з мерчантів на legacy-інтеграції.",
		Fixture: []Finding{
			{Claim: "37 мерчантів досі на legacy API v1 (ISO 8583 через TCP)", Source: "crm:legacy-merchants-2026-10"},
			{Claim: "A-114 має кастомний callback, несумісний з новим форматом webhook", Source: "crm:A-114"},
		},
	},
}

// deskInstruction is the same contract for every specialist; only the tool differs.
func deskInstruction(s deskSpec) string {
	return fmt.Sprintf(`Ти — %s команди LEDGERWORKS.
1. Спершу ОБОВ'ЯЗКОВО виклич %s. Без виклику інструмента не відповідай.
2. Відповідай ЛИШЕ фактами з відповіді інструмента. Не додавай власних знань.
3. Кожен факт — окремим рядком, з джерелом у дужках, наприклад: (incident-db:INC-418).
4. Якщо інструмент повернув помилку — напиши «ДЖЕРЕЛО НЕ ВІДПОВІЛО» і код помилки. Не вигадуй факти замість нього.`,
		s.Description, s.Tool)
}

// lookupRisk — фікстура бази інцидентів LEDGERWORKS. Справжня система
// ходила б у incident-db; форма (факт + джерело) та сама.
func lookupRisk(_ agent.Context, _ deskInput) (deskOutput, error) {
	return deskOutput{Findings: desks[riskDesk].Fixture}, nil
}

// lookupFor returns the fixture tool of a desk. down makes the source silent.
func lookupFor(s deskSpec, down bool) func(agent.Context, deskInput) (deskOutput, error) {
	return func(_ agent.Context, _ deskInput) (deskOutput, error) {
		if down {
			return deskOutput{}, &SourceError{Code: s.ErrCode, Source: s.Source}
		}
		return deskOutput{Findings: s.Fixture}, nil
	}
}

// toolOutcome is what the tool really returned, kept in session state under
// outcomeKey. The section is built from it, never from the model's prose.
type toolOutcome struct {
	OK       bool      `json:"ok"`
	Findings []Finding `json:"findings,omitempty"`
	Code     string    `json:"code,omitempty"`
	Error    string    `json:"error,omitempty"`
}

func outcomeKey(desk string) string { return "desk:" + desk }

// recordOutcome runs after the tool and saves its result or its error.
// An AfterToolCallback sees the Go error, the model only sees {"error": ...}.
func recordOutcome(desk string) llmagent.AfterToolCallback {
	return func(ctx agent.Context, _ tool.Tool, _, result map[string]any, err error) (map[string]any, error) {
		var out toolOutcome
		if err != nil {
			out = toolOutcome{Code: "TOOL_ERROR", Error: err.Error()}
			if se, ok := errors.AsType[*SourceError](err); ok {
				out.Code, out.Error = se.Code, "джерело "+se.Source+" не відповіло"
			}
		} else {
			var parsed deskOutput
			raw, mErr := json.Marshal(result)
			if mErr == nil {
				mErr = json.Unmarshal(raw, &parsed)
			}
			if mErr != nil {
				out = toolOutcome{Code: "BAD_TOOL_OUTPUT", Error: mErr.Error()}
			} else {
				out = toolOutcome{OK: true, Findings: parsed.Findings}
			}
		}
		raw, mErr := json.Marshal(out)
		if mErr != nil {
			return nil, fmt.Errorf("%s: outcome: %w", desk, mErr)
		}
		if sErr := ctx.State().Set(outcomeKey(desk), string(raw)); sErr != nil {
			return nil, fmt.Errorf("%s: save outcome: %w", desk, sErr)
		}
		return nil, nil
	}
}

// newSpecialist builds one llmagent with its own tool. down simulates a
// silent source for containment tests and the -down flag.
func newSpecialist(s deskSpec, m model.LLM, down bool) (agent.Agent, error) {
	t, err := functiontool.New(functiontool.Config{Name: s.Tool, Description: s.ToolDesc}, lookupFor(s, down))
	if err != nil {
		return nil, fmt.Errorf("%s tool: %w", s.Name, err)
	}
	a, err := llmagent.New(llmagent.Config{
		Name:               s.Name,
		Model:              m,
		Description:        s.Description,
		Instruction:        deskInstruction(s),
		Tools:              []tool.Tool{t},
		AfterToolCallbacks: []llmagent.AfterToolCallback{recordOutcome(s.Name)},
	})
	if err != nil {
		return nil, fmt.Errorf("%s agent: %w", s.Name, err)
	}
	return a, nil
}
