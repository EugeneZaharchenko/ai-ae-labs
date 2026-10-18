package react

import (
	"context"
	"fmt"
	"io"

	"google.golang.org/adk/v2/model"

	"github.com/dimetron/ai-eng-course/labs/internal/modelcfg"
)

// ResolveModel picks the real model through the course-wide modelcfg
// (apps/.env, MODEL, DEFAULT_MODEL_PROVIDER) and says what it chose.
//
// ok=false is not an error of the lab: it means no provider is configured,
// and the caller falls back to the offline path. The cause is written to out,
// never swallowed — an offline run must never be mistaken for a live one.
func ResolveModel(ctx context.Context, out io.Writer) (m model.LLM, ok bool) {
	_ = modelcfg.LoadEnv(".")
	m, choice, err := modelcfg.Load(ctx)
	if err != nil {
		fmt.Fprintf(out, "модель: офлайн (провайдера не знайдено — %v)\n", firstLine(err.Error()))
		return nil, false
	}
	fmt.Fprintf(out, "модель: %s / %s (%s)\n", choice.Provider, choice.Model, choice.Reason)
	return m, true
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
