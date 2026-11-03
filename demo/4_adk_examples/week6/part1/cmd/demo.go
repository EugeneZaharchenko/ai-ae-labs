package main

import (
	"context"
	"iter"
	"regexp"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// The offline stand-in model.
//
// It plays both sides of the loop, told apart by the "ROLE:" line in the system
// instruction, and — as in the other parts — it reads its own transcript rather
// than replaying a script: a script would answer the same whether the critics
// caught the bug or not, and that is the subject.
//
// The maker is deliberately hurried. Its first candidate is the float64 version:
// correct on every case the visible tests list, wrong on large amounts — bug #423.
// Given FEEDBACK it writes the math/big version. The correctness critic flags
// float64 in the candidate; the other two always pass. A live model may or may
// not reproduce this; the stand-in always does, so both the catch and the miss
// (-critics=false) are reachable offline on every run.
//
// It reports no usage on purpose: fabricated token counts would make the bill a lie.
type demoModel struct{}

func newDemoModel() *demoModel { return &demoModel{} }

func (d *demoModel) Name() string { return "offline-maker-critic-stand-in" }

var roleRe = regexp.MustCompile(`ROLE: (\w+)`)

func (d *demoModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		role := ""
		if req.Config != nil && req.Config.SystemInstruction != nil {
			for _, p := range req.Config.SystemInstruction.Parts {
				if m := roleRe.FindStringSubmatch(p.Text); m != nil {
					role = m[1]
				}
			}
		}
		user := lastUserText(req.Contents)

		var out string
		switch Role(role) {
		case RoleCorrectness, RoleStyle, RolePerformance:
			out = d.critique(Role(role), user)
		default:
			out = d.make(user)
		}
		yield(&model.LLMResponse{
			Content:      genai.NewContentFromText(out, genai.RoleModel),
			FinishReason: genai.FinishReasonStop,
		}, nil)
	}
}

// make writes the buggy candidate first and the reference once it has FEEDBACK.
func (d *demoModel) make(prompt string) string {
	code := buggyCandidate
	if strings.Contains(prompt, "FEEDBACK") {
		code = referenceCandidate
	}
	return "```go\n" + code + "```"
}

// critique answers in the verdict schema. Only the part after "Candidate:" is
// inspected: the task text above it mentions float64 too, as a warning.
func (d *demoModel) critique(role Role, prompt string) string {
	_, candidate, _ := strings.Cut(prompt, "Candidate:")
	if role == RoleCorrectness && strings.Contains(candidate, "float64") {
		return `{"critic":"correctness","passed":false,"comments":["float64 has a 53-bit mantissa: amounts above 2^53 lose cents, and int64(float64) overflows near MaxInt64. Do the multiplication and the half-up rounding in integer arithmetic (math/big) — this is bug #423."]}`
	}
	return `{"critic":"` + string(role) + `","passed":true}`
}

// lastUserText is the newest user text in the request: the current prompt.
func lastUserText(contents []*genai.Content) string {
	for i := len(contents) - 1; i >= 0; i-- {
		if contents[i].Role != genai.RoleUser {
			continue
		}
		var b strings.Builder
		for _, p := range contents[i].Parts {
			b.WriteString(p.Text)
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return ""
}
