package main

import (
	"context"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool/toolutils"

	"github.com/dimetron/ai-eng-course/labs/internal/fakellm"
)

// TestToolSchemaIsInferredFromTags is the test that settles the struct-tag
// question empirically instead of by assertion in a slide.
//
// It proves two things:
//  1. the `jsonschema` tag value becomes the parameter's DESCRIPTION;
//  2. both fields land in the schema with type "string".
//
// If someone "fixes" RateInput to use `jsonschema:"required,enum=USD"`, this
// test goes red with the literal string in the description — which is exactly
// the failure a learner would otherwise meet live on camera.
func TestToolSchemaIsInferredFromTags(t *testing.T) {
	rateTool, err := NewRateTool(fixture())
	if err != nil {
		t.Fatalf("NewRateTool() error = %v", err)
	}

	declarer, ok := rateTool.(toolutils.Tool)
	if !ok {
		t.Fatalf("tool %T does not expose Declaration(); cannot inspect schema", rateTool)
	}
	decl := declarer.Declaration()
	if decl == nil {
		t.Fatal("Declaration() = nil, want an inferred function declaration")
	}

	if decl.Name != "get_exchange_rate" {
		t.Errorf("Name = %q, want %q", decl.Name, "get_exchange_rate")
	}

	// Worth knowing: functiontool puts the inferred schema in
	// ParametersJsonSchema (typed `any`, holding *jsonschema.Schema), NOT in
	// the genai-native Parameters field, which stays nil.
	if decl.Parameters != nil {
		t.Errorf("Parameters = %+v, want nil (inference uses ParametersJsonSchema)", decl.Parameters)
	}
	schema, ok := decl.ParametersJsonSchema.(*jsonschema.Schema)
	if !ok {
		t.Fatalf("ParametersJsonSchema is %T, want *jsonschema.Schema", decl.ParametersJsonSchema)
	}
	if schema.Type != "object" {
		t.Errorf("schema.Type = %q, want %q", schema.Type, "object")
	}

	for _, field := range []string{"base", "target"} {
		prop, ok := schema.Properties[field]
		if !ok {
			t.Errorf("schema has no property %q", field)
			continue
		}
		if prop.Type != "string" {
			t.Errorf("property %q type = %q, want %q", field, prop.Type, "string")
		}
		if prop.Description == "" {
			t.Errorf("property %q has an empty description; the jsonschema tag did not reach the schema", field)
		}
		// The tag text is prose. If a constraint-style tag were used, the
		// description would read "required,enum=..." instead.
		if strings.Contains(prop.Description, "enum=") || strings.Contains(prop.Description, "required,") {
			t.Errorf("property %q description = %q; the jsonschema tag is a description, not a constraint list",
				field, prop.Description)
		}
	}
}

// TestToolDescriptionIsNonEmpty guards the other half of the contract: a tool
// the model cannot understand is a tool the model will not call.
func TestToolDescriptionIsNonEmpty(t *testing.T) {
	rateTool, err := NewRateTool(fixture())
	if err != nil {
		t.Fatalf("NewRateTool() error = %v", err)
	}
	if rateTool.Name() == "" {
		t.Error("tool name is empty")
	}
	if len(rateTool.Description()) < 20 {
		t.Errorf("tool description %q is too short to steer a model", rateTool.Description())
	}
}

func TestNewAgent(t *testing.T) {
	m := fakellm.New("fake", fakellm.TextTurn("ok"))

	a, err := NewAgent(m, fixture())
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	if a.Name() != "currency_agent" {
		t.Errorf("agent name = %q, want %q", a.Name(), "currency_agent")
	}
}

// --- Tool execution through the agent context -------------------------------

// toolCtx is a strict agent.Context fake. Embedding StrictContextMock means any
// method the tool calls that we have not thought about panics loudly rather
// than silently returning a zero value.
type toolCtx struct {
	agent.StrictContextMock
}

// TestToolHandlerRunsThroughAgentContext exercises the actual handler signature
// ADK requires — func(agent.Context, TArgs) (TResults, error) — rather than
// testing Convert in isolation.
func TestToolHandlerRunsThroughAgentContext(t *testing.T) {
	ctx := &toolCtx{StrictContextMock: agent.NewStrictContextMock(context.Background())}

	got, err := Convert(ctx, fixture(), RateInput{Base: "USD", Target: "UAH"})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if got.Rate != 41.5 {
		t.Errorf("Rate = %v, want 41.5", got.Rate)
	}
}

// TestFakeModelDrivesToolCall demonstrates the scripted-model pattern: assert
// what the agent sent the model, deterministically and for free.
func TestFakeModelDrivesToolCall(t *testing.T) {
	m := fakellm.New("fake",
		fakellm.CallTurn("get_exchange_rate", map[string]any{"base": "USD", "target": "UAH"}),
		fakellm.TextTurn("1 USD = 41.5 UAH (nbu, 2026-07-29)"),
	)

	if _, err := NewAgent(m, fixture()); err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}

	// The script is staged but unconsumed until a Runner drives the agent;
	// week 2 introduces the Runner. What this asserts today is that the fake
	// and the agent agree on the model interface at compile time, and that the
	// script is intact.
	if m.CallCount() != 0 {
		t.Errorf("CallCount() = %d before any run, want 0", m.CallCount())
	}
	if m.Remaining() != 2 {
		t.Errorf("Remaining() = %d, want 2 scripted turns", m.Remaining())
	}
	var _ model.LLM = m
}
