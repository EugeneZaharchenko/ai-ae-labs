package usage

import (
	"context"
	"errors"
	"iter"
	"testing"

	"google.golang.org/adk/v2/model"
)

// pipe is a model.LLM that replays canned responses, so the decorator can be
// tested without a provider or a network. It is not internal/scripted: that one
// is a whole conversation script, and here the point is only the shape of the
// stream.
type pipe struct {
	name string
	seq  []*model.LLMResponse
	err  error
}

func (p pipe) Name() string { return p.name }

func (p pipe) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		for _, r := range p.seq {
			if !yield(r, nil) {
				return
			}
		}
		if p.err != nil {
			yield(nil, p.err)
		}
	}
}

func TestMeteringCountsAStreamedCallOnce(t *testing.T) {
	// A provider streams a turn as several chunks. Counting each chunk would bill
	// one call once per chunk — the difference between a $4 and a $16 night, which
	// is the bill the lecture opens with.
	chunk := func(partial bool, u bool) *model.LLMResponse {
		r := &model.LLMResponse{Partial: partial}
		if u {
			r.UsageMetadata = meta(100, 50, 0)
		}
		return r
	}
	var m Meter
	metered := Metering{
		Inner: pipe{name: "streaming", seq: []*model.LLMResponse{
			chunk(true, true), chunk(true, true), chunk(true, true),
			// The finished chunk carries no usage of its own — a real shape.
			chunk(false, false),
		}},
		Meter: &m,
	}

	for range metered.GenerateContent(context.Background(), &model.LLMRequest{}, true) {
	}

	got := m.Total()
	if got.Calls != 1 {
		t.Errorf("a streamed call is one call: got %d", got.Calls)
	}
	if got.Prompt != 100 || got.Output != 50 {
		t.Errorf("usage from the partials was not hoisted onto the finished chunk: %+v", got)
	}
}

func TestMeteringCountsEachTurnSeparately(t *testing.T) {
	var m Meter
	metered := Metering{
		Inner: pipe{name: "two-turns", seq: []*model.LLMResponse{
			{UsageMetadata: meta(10, 5, 0)},
			{UsageMetadata: meta(20, 5, 0)},
		}},
		Meter: &m,
	}
	for range metered.GenerateContent(context.Background(), &model.LLMRequest{}, false) {
	}
	if got := m.Total(); got.Calls != 2 || got.Total() != 40 {
		t.Errorf("want 2 calls totalling 40, got %+v", got)
	}
}

func TestMeteringCountsACallThatReportsNoUsage(t *testing.T) {
	// A provider that reports nothing is still a call. Dropping it would hide the
	// one number that shows a loop is running away.
	var m Meter
	metered := Metering{Inner: pipe{name: "silent", seq: []*model.LLMResponse{{}}}, Meter: &m}
	for range metered.GenerateContent(context.Background(), &model.LLMRequest{}, false) {
	}
	if got := m.Total(); got.Calls != 1 || got.Total() != 0 {
		t.Errorf("want 1 call totalling 0, got %+v", got)
	}
}

func TestMeteringPassesEverythingThrough(t *testing.T) {
	// The decorator must not rewrite the stream: the model's name is how the
	// provider is selected, and the responses are what the agent reads.
	var m Meter
	seq := []*model.LLMResponse{
		{Partial: true, Content: nil},
		{UsageMetadata: meta(1, 1, 0)},
	}
	metered := Metering{Inner: pipe{name: "gemini-3.8-flash", seq: seq}, Meter: &m}

	if metered.Name() != "gemini-3.8-flash" {
		t.Errorf("the decorator renamed the model: %q", metered.Name())
	}
	var got int
	for range metered.GenerateContent(context.Background(), &model.LLMRequest{}, false) {
		got++
	}
	if got != len(seq) {
		t.Errorf("want %d responses through, got %d", len(seq), got)
	}
}

func TestMeteringDoesNotCountAFailedCall(t *testing.T) {
	// The error has to reach the caller — a decorator that swallowed a provider
	// failure would turn a crash into a silent empty answer — and a call that
	// never happened must not appear on the bill.
	var m Meter
	metered := Metering{
		Inner: pipe{name: "broken", err: errors.New("upstream 503")},
		Meter: &m,
	}

	var seen error
	for _, err := range metered.GenerateContent(context.Background(), &model.LLMRequest{}, false) {
		if err != nil {
			seen = err
		}
	}
	if seen == nil {
		t.Fatal("the decorator swallowed the provider error")
	}
	if got := m.Total(); got.Calls != 0 {
		t.Errorf("want no calls counted for a call that never completed, got %+v", got)
	}
}
