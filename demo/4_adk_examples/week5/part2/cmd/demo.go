package main

import (
	"context"
	"iter"
	"regexp"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// The offline stand-in desks.
//
// Each desk's request carries its own fact sheet in the system instruction,
// and the stand-in answers by quoting that sheet back — so the brief is
// grounded by construction, and a test can check that each section came from
// its own desk's facts and from no one else's. That is the isolation property
// the graph is supposed to give, observed rather than assumed.
//
// It reports no usage on purpose; see week 4 part 2.
type demoModel struct{}

func newDemoModel() *demoModel { return &demoModel{} }

func (d *demoModel) Name() string { return "offline-desk-stand-in" }

var bulletRe = regexp.MustCompile(`(?m)^- .+$`)

func (d *demoModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var sys string
		if req.Config != nil && req.Config.SystemInstruction != nil {
			for _, p := range req.Config.SystemInstruction.Parts {
				sys += p.Text
			}
		}
		text := strings.Join(bulletRe.FindAllString(sys, -1), "\n")
		if text == "" {
			text = "- The facts provided do not answer this."
		}
		yield(&model.LLMResponse{
			Content:      genai.NewContentFromText(text, genai.RoleModel),
			FinishReason: genai.FinishReasonStop,
		}, nil)
	}
}
