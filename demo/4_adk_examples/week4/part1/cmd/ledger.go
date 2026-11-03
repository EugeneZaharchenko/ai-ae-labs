package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"google.golang.org/genai"
)

// The LEDGERWORKS world.
//
// This is a payment processor's payout desk, and the incident the example
// investigates is the one the labs use all week: merchant A-114 has not been
// paid, because settlement batch #422 is held.
//
// The data is small but it is shaped like the real thing, and that shape is the
// point. Answering "why is A-114 not paid" takes four dependent calls —
// payouts → batch → wiki → ticket — because no single tool holds the answer.
// A loop that picks tools round-robin, as the first draft of this example did,
// cannot answer it at all; only a model reading each observation and choosing
// the next call can. That is what makes this a ReAct loop rather than a for
// statement.
//
// Ids match the labs (Day 7 onwards): merchant A-114, settled batch #418,
// incident INC-418, tickets LDG-N. An example and its lab should not need a
// translation table.

// World is the mutable state behind the tools. Tools read it, and the one
// side-effecting tool appends to it, so a test can assert on what the agent
// actually did rather than only on what it said.
type World struct {
	mu      sync.Mutex
	tickets []Ticket
}

// Ticket is what create_jira_ticket produces.
type Ticket struct {
	Key     string `json:"key"`
	Summary string `json:"summary"`
	Detail  string `json:"detail"`
}

var (
	merchants = map[string]merchant{
		"A-114": {ID: "A-114", Name: "Kvitka Market", Tier: "gold", PayoutAPI: "legacy-v1", Acquirer: "Acquirer X"},
		"A-207": {ID: "A-207", Name: "Dnipro Fuel", Tier: "standard", PayoutAPI: "payouts-v3", Acquirer: "Acquirer X"},
		"A-338": {ID: "A-338", Name: "Odesa Books", Tier: "standard", PayoutAPI: "payouts-v3", Acquirer: "Nordic Pay"},
	}

	batches = map[string]batch{
		// The batches the incident is about: held, and the hold reason names an
		// incident id that only the wiki knows anything about. Two of them, so
		// the answer has to generalise rather than name one lucky batch.
		"422": {ID: "422", Status: "held", OpenedAt: "2026-10-13T03:10:00Z", CutoffAt: "2026-10-13T05:00:00Z",
			HoldReason: "risk review INC-418 pending"},
		"421": {ID: "421", Status: "held", OpenedAt: "2026-10-12T03:10:00Z", CutoffAt: "2026-10-12T05:00:00Z",
			HoldReason: "risk review INC-418 pending"},
		"418": {ID: "418", Status: "settled", OpenedAt: "2026-10-11T02:41:00Z", CutoffAt: "2026-10-11T05:00:00Z"},
	}

	payouts = []payout{
		{ID: "P-8891", Merchant: "A-114", Batch: "422", AmountMinor: 1_824_000, Currency: "EUR", Status: "pending"},
		{ID: "P-8892", Merchant: "A-114", Batch: "421", AmountMinor: 911_550, Currency: "EUR", Status: "pending"},
		{ID: "P-8870", Merchant: "A-114", Batch: "418", AmountMinor: 3_100_200, Currency: "EUR", Status: "settled"},
		{ID: "P-8901", Merchant: "A-207", Batch: "422", AmountMinor: 554_000, Currency: "EUR", Status: "pending"},
		{ID: "P-8902", Merchant: "A-338", Batch: "418", AmountMinor: 1_295_000, Currency: "EUR", Status: "settled"},
	}

	wiki = []wikiPage{
		{ID: "INC-418", Title: "Incident INC-418: acquirer cutoff mismatch on the legacy payout API",
			Tags: []string{"incident", "payouts", "legacy"},
			Body: "Between 2026-10-11 and 2026-10-13 the legacy payout API (v1) reported its batch cutoff " +
				"in local time while the settlement scheduler read it as UTC. Batches whose merchant is still " +
				"on v1 were therefore held for risk review instead of being cut off. Impact: 3 merchants. " +
				"Resolution: re-queue the affected batch from the payouts-v3 console. Merchants on " +
				"payouts-v3 were never affected."},
		{ID: "RB-12", Title: "Runbook RB-12: re-queue a held settlement batch",
			Tags: []string{"runbook", "payouts"},
			Body: "A held batch is released by re-queuing it from the payouts-v3 console. Do not edit the " +
				"batch's cutoff by hand. If the hold names a risk review, the review must be closed first."},
		{ID: "RB-07", Title: "Runbook RB-07: merchant payout API migration",
			Tags: []string{"runbook", "migration"},
			Body: "Migrating a merchant from legacy-v1 to payouts-v3 takes one settlement cycle and needs " +
				"no merchant-side change. Migrate merchants that appear in more than one legacy-batch incident."},
	}
)

type merchant struct {
	ID, Name, Tier, PayoutAPI, Acquirer string
}

type batch struct {
	ID, Status, OpenedAt, CutoffAt, HoldReason string
}

type payout struct {
	ID, Merchant, Batch, Currency, Status string
	AmountMinor                           int64
}

type wikiPage struct {
	ID, Title, Body string
	Tags            []string
}

// Tool is one capability the agent may call. Params is the JSON schema that
// travels to the provider as a function declaration — the model is told the
// tools' shape, not their implementation.
type Tool struct {
	Name          string
	Description   string
	Params        *genai.Schema
	SideEffecting bool
	Run           func(w *World, args map[string]any) (string, error)
}

// tools is the registry. Independent read-only tools can be called together in
// one model turn; the side-effecting one is marked so the loop can refuse to
// run it twice for the same case.
func tools() []Tool {
	return []Tool{
		{
			Name:        "search_internal_wiki",
			Description: "Search LEDGERWORKS internal runbooks and incident postmortems. Returns matching pages with their ids.",
			Params: obj(map[string]*genai.Schema{
				"query": str("Words to match against page titles, bodies and tags."),
			}, "query"),
			Run: runSearchWiki,
		},
		{
			Name:        "get_merchant_payouts",
			Description: "List the recent payouts of one merchant, newest first, with the settlement batch each belongs to.",
			Params: obj(map[string]*genai.Schema{
				"merchant_id": str("Merchant id, in the form A-114."),
			}, "merchant_id"),
			Run: runGetPayouts,
		},
		{
			Name:        "get_settlement_batch",
			Description: "Read one settlement batch: its status, its cutoff time, and the reason it is held if it is.",
			Params: obj(map[string]*genai.Schema{
				"batch_id": str("Settlement batch id, e.g. 422."),
			}, "batch_id"),
			Run: runGetBatch,
		},
		{
			Name: "get_merchant",
			Description: "Read one merchant's profile: name, tier, acquirer, and which payout API they are on. " +
				"A merchant on legacy-v1 is affected by the cutoff bug described in INC-418.",
			Params: obj(map[string]*genai.Schema{
				"merchant_id": str("Merchant id, in the form A-114."),
			}, "merchant_id"),
			Run: runGetMerchant,
		},
		{
			Name:          "create_jira_ticket",
			Description:   "Open a Jira ticket for the payout desk. Changes state in the outside world; call it at most once per incident, and only once you know the cause.",
			SideEffecting: true,
			Params: obj(map[string]*genai.Schema{
				"summary": str("One-line title."),
				"detail":  str("What is wrong, which merchant and batch are affected, and what the fix is."),
			}, "summary", "detail"),
			Run: runCreateTicket,
		},
	}
}

func runSearchWiki(_ *World, args map[string]any) (string, error) {
	q := strings.TrimSpace(argString(args, "query"))
	if q == "" {
		return "", fmt.Errorf("query is required")
	}
	terms := strings.Fields(strings.ToLower(q))
	type hit struct {
		page  wikiPage
		score int
	}
	var hits []hit
	for _, p := range wiki {
		hay := strings.ToLower(p.ID + " " + p.Title + " " + p.Body + " " + strings.Join(p.Tags, " "))
		score := 0
		for _, t := range terms {
			if strings.Contains(hay, t) {
				score++
			}
		}
		if score > 0 {
			hits = append(hits, hit{p, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) == 0 {
		return fmt.Sprintf("No wiki page matches %q. Known ids: INC-418, RB-12, RB-07.", q), nil
	}
	var b strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&b, "[%s] %s\n%s\n\n", h.page.ID, h.page.Title, h.page.Body)
	}
	return strings.TrimSpace(b.String()), nil
}

func runGetPayouts(_ *World, args map[string]any) (string, error) {
	id := strings.TrimSpace(argString(args, "merchant_id"))
	if _, ok := merchants[id]; !ok {
		return "", fmt.Errorf("unknown merchant %q (known merchants look like A-114; also A-207, A-338)", id)
	}
	var mine []payout
	for _, p := range payouts {
		if p.Merchant == id {
			mine = append(mine, p)
		}
	}
	if len(mine) == 0 {
		return fmt.Sprintf("Merchant %s has no payouts on record.", id), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Payouts for %s (%s), newest first:\n", id, merchants[id].Name)
	for _, p := range mine {
		fmt.Fprintf(&b, "- %s batch#%s %s %.2f %s\n", p.ID, p.Batch, p.Status, float64(p.AmountMinor)/100, p.Currency)
	}
	return strings.TrimSpace(b.String()), nil
}

func runGetBatch(_ *World, args map[string]any) (string, error) {
	id := strings.TrimSpace(argString(args, "batch_id"))
	b, ok := batches[id]
	if !ok {
		return "", fmt.Errorf("unknown settlement batch %q (known: 418, 421, 422)", id)
	}
	out := fmt.Sprintf("Batch #%s status=%s opened=%s cutoff=%s", b.ID, b.Status, b.OpenedAt, b.CutoffAt)
	if b.HoldReason != "" {
		out += "\nhold_reason: " + b.HoldReason
	}
	return out, nil
}

func runGetMerchant(_ *World, args map[string]any) (string, error) {
	id := strings.TrimSpace(argString(args, "merchant_id"))
	m, ok := merchants[id]
	if !ok {
		return "", fmt.Errorf("unknown merchant %q (known merchants look like A-114; also A-207, A-338)", id)
	}
	return fmt.Sprintf("Merchant %s: name=%q tier=%s acquirer=%s payout_api=%s",
		m.ID, m.Name, m.Tier, m.Acquirer, m.PayoutAPI), nil
}

func runCreateTicket(w *World, args map[string]any) (string, error) {
	summary := strings.TrimSpace(argString(args, "summary"))
	if summary == "" {
		return "", fmt.Errorf("summary is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	key := fmt.Sprintf("LDG-%d", len(w.tickets)+1)
	t := Ticket{Key: key, Summary: summary, Detail: strings.TrimSpace(argString(args, "detail"))}
	w.tickets = append(w.tickets, t)
	return "Created ticket " + key + ": " + summary, nil
}

// OpenTickets returns what the agent opened, so a test can assert on the
// effect rather than on the prose.
func (w *World) OpenTickets() []Ticket {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Ticket(nil), w.tickets...)
}

func argString(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return strings.Trim(string(b), `"`)
}

func obj(props map[string]*genai.Schema, required ...string) *genai.Schema {
	return &genai.Schema{Type: genai.TypeObject, Properties: props, Required: required}
}

func str(desc string) *genai.Schema {
	return &genai.Schema{Type: genai.TypeString, Description: desc}
}

// findTool returns the registry entry by name.
func findTool(name string) (Tool, bool) {
	for _, t := range tools() {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// toolNames lists the registry, for the error a model gets when it invents one.
func toolNames() string {
	names := make([]string, 0, len(tools()))
	for _, t := range tools() {
		names = append(names, t.Name)
	}
	return strings.Join(names, ", ")
}

// The investigation the example runs by default. It is deliberately answerable
// only by chaining: the payouts name a batch, the batch names an incident, and
// only the wiki explains that incident.
const defaultTask = "Merchant A-114 says they have not been paid. Find out why, and open a ticket if the cause is on our side."
