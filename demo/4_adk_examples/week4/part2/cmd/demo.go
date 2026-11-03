package main

import (
	"context"
	"fmt"
	"iter"
	"regexp"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// The offline stand-in model.
//
// Why this exists rather than internal/scripted: the scripted double replays a
// fixed list of turns, so it answers the same thing whatever the tool said. That is
// exactly what a harness cannot be demonstrated with — the whole subject is what
// the model does NEXT, given what the harness told it. So this stand-in reads the
// last open_refund_case observation out of its own conversation and decides, the
// same way a model does, just with regexps instead of a forward pass.
//
// It is deliberately careless on the first call: it sends the literal string
// "undefined" for both ids. That is not a strawman — it is the documented incident
// this example is drawn from, where a model with no merchant id wrote a placeholder
// instead of asking, and a validator that only checked for emptiness let it into
// production at 03:00. A live model may or may not reproduce it; the stand-in
// always does, so the invalid_argument path is reachable offline on every run.
//
// It reports no usage on purpose. Fabricating token counts would make the bill in
// the report a lie, and a report you have to distrust is worse than a report that
// says "4 calls, tokens only in live mode".
type demoModel struct {
	calls int
}

func newDemoModel() *demoModel { return &demoModel{} }

// Name is what the report prints alongside the answer. It says what it is.
func (d *demoModel) Name() string { return "offline-refund-stand-in" }

// Scanning patterns. Deliberately NOT anchored, unlike ledger's Validate: this is
// a search through prose for an id that might be in there, which is the model's job,
// where Validate checks one whole field, which is the tool's. Confusing the two is
// how a placeholder ends up passing validation.
var (
	scanTxn      = regexp.MustCompile(`\btxn-\d{4}-\d{2}-\d+\b`)
	scanMerchant = regexp.MustCompile(`\b[A-Z]-\d{2,}\b`)
)

func (d *demoModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var resp *model.LLMResponse

		switch obs := lastObservation(req.Contents); {
		case obs == nil:
			// First turn: no observation yet, so no tool has run. Call it carelessly.
			resp = d.call(RefundCaseInput{TransactionID: "undefined", MerchantID: "undefined"})

		default:
			resp = d.after(req.Contents, obs)
		}
		yield(resp, nil)
	}
}

// after is the model's decision, given the tool's last answer.
func (d *demoModel) after(contents []*genai.Content, obs map[string]any) *model.LLMResponse {
	switch status, _ := obs["status"].(string); status {
	case "pending":
		caseID, _ := obs["case_id"].(string)
		note := "The refund case " + caseID + " is open."
		if replayed, _ := obs["replayed"].(bool); replayed {
			note += " The case already existed, so this was a replay rather than a second case."
		}
		return text(note)

	case "invalid_argument":
		// The harness named a field. The fix is to re-read the request and lift the
		// ids out of it — and to ask rather than invent when they are not there,
		// which is the one thing the first call got wrong.
		src := taskText(contents)
		in := RefundCaseInput{
			TransactionID: firstMatch(scanTxn, src),
			MerchantID:    firstMatch(scanMerchant, src),
		}
		if in.TransactionID == "" || in.MerchantID == "" {
			return text("I do not have " + missing(in) + " for this refund. Could you send " +
				"the transaction id (they look like txn-2026-07-118845) and the merchant id " +
				"(they look like A-114)?")
		}
		return d.call(in)

	case "unavailable":
		return text("I could not open the refund case. " + observation(obs) +
			" A retry from me would not help, so I am reporting it rather than trying again.")

	case "stopped":
		return text("I stopped without opening a case. " + observation(obs))

	default:
		// A fatal error never arrives as a status: ADK converts the handler's error
		// into an observation of its own and the run carries on, so the shape here is
		// {"error": ...} with no status at all. Answering it is the model's part; the
		// decision to fail the run belongs to the caller, which is what main's exit
		// code is for.
		if msg, ok := obs["error"].(string); ok {
			return text("The tool failed: " + msg + " I am stopping rather than retrying, " +
				"and this needs someone to look at it.")
		}
		return text("The tool answered something this stand-in does not know how to handle: " +
			fmt.Sprint(obs))
	}
}

// call emits one tool call. The id is unique per call so ADK can pair the
// FunctionResponse with it — an empty id would leave the correlation to chance.
func (d *demoModel) call(in RefundCaseInput) *model.LLMResponse {
	d.calls++
	return &model.LLMResponse{
		Content: genai.NewContentFromParts([]*genai.Part{{
			FunctionCall: &genai.FunctionCall{
				ID:   fmt.Sprintf("call-%d", d.calls),
				Name: ToolName,
				Args: map[string]any{
					"transaction_id": in.TransactionID,
					"merchant_id":    in.MerchantID,
				},
			},
		}}, genai.RoleModel),
		FinishReason: genai.FinishReasonStop,
	}
}

// text ends the run: a turn with no function call is the agent's final answer.
func text(s string) *model.LLMResponse {
	return &model.LLMResponse{
		Content:      genai.NewContentFromText(s, genai.RoleModel),
		FinishReason: genai.FinishReasonStop,
	}
}

// lastObservation finds the most recent result of this tool. Reading the
// conversation rather than keeping a counter of our own is the point: a model has
// no memory except this transcript, so anything it knows it read from here.
func lastObservation(contents []*genai.Content) map[string]any {
	for i := len(contents) - 1; i >= 0; i-- {
		parts := contents[i].Parts
		for j := len(parts) - 1; j >= 0; j-- {
			if fr := parts[j].FunctionResponse; fr != nil && fr.Name == ToolName {
				return fr.Response
			}
		}
	}
	return nil
}

// taskText is everything the user said, so the stand-in can re-read the request
// the way the model would.
func taskText(contents []*genai.Content) string {
	var b strings.Builder
	for _, c := range contents {
		if c.Role != genai.RoleUser {
			continue
		}
		for _, p := range c.Parts {
			if p.FunctionResponse == nil && p.Text != "" {
				b.WriteString(p.Text)
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

func firstMatch(re *regexp.Regexp, s string) string { return re.FindString(s) }

func observation(obs map[string]any) string {
	if s, _ := obs["observation"].(string); s != "" {
		return s
	}
	return "The tool did not say why."
}

func missing(in RefundCaseInput) string {
	switch {
	case in.TransactionID == "" && in.MerchantID == "":
		return "the transaction id or the merchant id"
	case in.TransactionID == "":
		return "the transaction id"
	default:
		return "the merchant id"
	}
}
