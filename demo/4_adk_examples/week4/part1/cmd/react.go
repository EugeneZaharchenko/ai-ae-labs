package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/dimetron/ai-eng-course/labs/internal/usage"
)

// The hand-written ReAct loop.
//
// Week 4's point is that an agent loop is not magic: it is a model call, a
// branch on whether the model asked for a tool, the tool results appended back,
// and a stop rule. Everything ADK does later in the week is this loop with the
// bookkeeping made declarative. So this file writes it out — no llmagent, no
// planner interface, no framework — and a reader can see all of it at once.
//
// What it is not is a toy. The model is a real one (internal/modelcfg), the
// tools are real Go functions over real state, and the run reports the tokens it
// actually spent (internal/usage). Only the tests swap the model, for a scripted
// one (internal/scripted); everything else in the loop runs for real.

// maxIterations bounds the loop. A ReAct loop without a cap is a loop that runs
// until the budget is gone, so the cap is a stop rule, not a formality.
const maxIterations = 8

// ErrStepCap reports a run that hit maxIterations without a final answer.
var ErrStepCap = errors.New("react: step cap reached with no final answer")

// systemPrompt does the work the loop cannot. It says what kind of agent this
// is, that independent reads may be batched into one step, that identifiers must
// come from evidence rather than invention, and how to stop. Each of those is a
// failure mode this shape of loop has actually exhibited.
const systemPrompt = `You are the investigation agent of LEDGERWORKS, a payment processor.
Use the tools to gather evidence. Call independent read-only tools together in ONE step.
Never invent identifiers: use only merchant, batch and incident ids that appear in the task or in tool results.
When you have enough evidence, answer in two sentences and call no tool.
You only handle LEDGERWORKS payout incidents; refuse anything else.`

// investigator is the loop's state, in one struct rather than a set of closures,
// so a test can build one around a scripted model and a throwaway World and then
// inspect what it did.
type investigator struct {
	model model.LLM
	world *World
	meter *usage.Meter

	// mu guards the two fields below. A step's tool calls run concurrently —
	// see dispatch — so anything they share is genuinely shared.
	mu           sync.Mutex
	counts       map[string]int
	sideEffected bool
}

// run adapts the loop to workflow.DynamicFn[string, string]. Hosting a
// hand-written loop inside an ADK dynamic node is the bridge to the rest of the
// week: the loop is still the loop, but its progress and its result now travel
// as events, which is how the rest of the framework will see them.
func (in *investigator) run(ctx agent.Context, task string, emit func(*session.Event) error) (string, error) {
	return in.loop(ctx, ctx.InvocationID(), task, emit)
}

// loop is the loop itself. It takes a plain context and an invocation id rather
// than an agent.Context so a test can drive it with no runner at all.
func (in *investigator) loop(ctx context.Context, invocationID, task string, emit func(*session.Event) error) (string, error) {
	in.counts = map[string]int{}

	contents := []*genai.Content{genai.NewContentFromText(task, genai.RoleUser)}

	for step := 1; step <= maxIterations; step++ {
		resp, err := in.step(ctx, contents, step)
		if err != nil {
			return "", err
		}
		in.meter.Add(resp.UsageMetadata)

		// The assistant turn goes back verbatim. Dropping its function calls,
		// or rebuilding it from the results, is how a loop ends up with a model
		// that cannot see what it already asked for.
		contents = append(contents, resp.Content)

		calls := functionCalls(resp.Content)
		if len(calls) == 0 {
			return finalAnswer(resp.Content, step)
		}

		if err := in.report(ctx, invocationID, step, calls, emit); err != nil {
			return "", err
		}

		// One user-role turn carrying every result, each tagged with the id of
		// the call it answers. Those ids are the correlation: without them a
		// provider that batches calls cannot tell a result from its neighbour's,
		// and a parallel step silently mismatches.
		contents = append(contents, genai.NewContentFromParts(in.dispatch(calls), genai.RoleUser))
	}

	return "", fmt.Errorf("%w after %d steps (%s)", ErrStepCap, maxIterations, usage.PerTool(in.Counts()))
}

// step makes one model call. The request is the whole prompt: the system
// instruction, the tool declarations, and the conversation so far.
func (in *investigator) step(ctx context.Context, contents []*genai.Content, n int) (*model.LLMResponse, error) {
	req := &model.LLMRequest{
		Model:    in.model.Name(),
		Contents: contents,
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText(systemPrompt, genai.RoleUser),
			Tools:             []*genai.Tool{{FunctionDeclarations: declarations()}},
			ToolConfig: &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{
				Mode: genai.FunctionCallingConfigModeAuto,
			}},
		},
	}

	// A provider may stream. This loop wants the finished turn, not the deltas,
	// so it keeps the last complete response and hoists usage off whichever
	// chunk happened to carry it.
	var last *model.LLMResponse
	var meta *genai.GenerateContentResponseUsageMetadata
	for resp, err := range in.model.GenerateContent(ctx, req, false) {
		if err != nil {
			return nil, fmt.Errorf("react: step %d: model call failed: %w", n, err)
		}
		if resp == nil {
			continue
		}
		if resp.UsageMetadata != nil {
			meta = resp.UsageMetadata
		}
		if !resp.Partial {
			last = resp
		}
	}
	if last == nil {
		return nil, fmt.Errorf("react: step %d: model returned no response", n)
	}
	if last.UsageMetadata == nil {
		last.UsageMetadata = meta
	}
	return last, nil
}

// dispatch runs a step's tool calls concurrently and returns one FunctionResponse
// part per call, in the order the model asked for them.
//
// The concurrency is not a flourish: it is what ADK does, it is why one step can
// gather four pieces of evidence in the time of one, and it is why whatever these
// tools share has to be guarded.
func (in *investigator) dispatch(calls []*genai.FunctionCall) []*genai.Part {
	results := make([]string, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = in.invoke(call)
		}()
	}
	wg.Wait()

	// Counting happens here, serially, so the tally is never shared state.
	in.mu.Lock()
	for _, c := range calls {
		in.counts[c.Name]++
	}
	in.mu.Unlock()

	parts := make([]*genai.Part, len(calls))
	for i, c := range calls {
		parts[i] = &genai.Part{FunctionResponse: &genai.FunctionResponse{
			ID: c.ID, Name: c.Name, Response: map[string]any{"result": results[i]},
		}}
	}
	return parts
}

// invoke calls one tool and turns its outcome into an observation.
//
// A tool error is an observation, not a crash — that is the difference between an
// agent loop and a pipeline. The model is told what went wrong and gets to try
// something else.
func (in *investigator) invoke(call *genai.FunctionCall) string {
	tool, ok := findTool(call.Name)
	if !ok {
		return fmt.Sprintf("ERROR: no such tool %q. Available tools: %s.", call.Name, toolNames())
	}
	if tool.SideEffecting {
		// The guard belongs to the loop, not the tool: a tool cannot know whether
		// this is the first call or the fourth, and a model that has lost track of
		// the conversation will happily open the same ticket again.
		in.mu.Lock()
		already := in.sideEffected
		in.sideEffected = true
		in.mu.Unlock()
		if already {
			return "ERROR: this run has already made its one state-changing call. " +
				"Answer from the evidence you already have."
		}
	}
	out, err := tool.Run(in.world, call.Args)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	return out
}

// report publishes one progress line per tool call, so a reader watching the
// console sees the model's choices as they happen rather than only the answer.
// Week 7's observability module is this idea with somewhere to send it.
func (in *investigator) report(ctx context.Context, invocationID string, step int, calls []*genai.FunctionCall, emit func(*session.Event) error) error {
	if emit == nil {
		return nil
	}
	for _, c := range calls {
		ev := session.NewEvent(ctx, invocationID)
		ev.LLMResponse.Content = genai.NewContentFromText(
			fmt.Sprintf("%d. %s(%s)", step, c.Name, briefArgs(c.Args)), genai.RoleModel)
		if err := emit(ev); err != nil {
			return err
		}
	}
	return nil
}

// Counts reports how often each tool was called.
func (in *investigator) Counts() map[string]int {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := make(map[string]int, len(in.counts))
	for k, v := range in.counts {
		out[k] = v
	}
	return out
}

// finalAnswer is the stop rule: no tool call means the model is done.
func finalAnswer(c *genai.Content, step int) (string, error) {
	if c == nil {
		return "", fmt.Errorf("react: step %d: empty response", step)
	}
	var b strings.Builder
	for _, p := range c.Parts {
		if p.Text != "" {
			b.WriteString(p.Text)
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return "", fmt.Errorf("react: step %d: model stopped without answering", step)
	}
	return strings.TrimSpace(b.String()), nil
}

func functionCalls(c *genai.Content) []*genai.FunctionCall {
	if c == nil {
		return nil
	}
	var out []*genai.FunctionCall
	for _, p := range c.Parts {
		if p.FunctionCall != nil {
			out = append(out, p.FunctionCall)
		}
	}
	return out
}

// declarations turns the registry into the function declarations the provider
// is given. The model sees the tools' names, descriptions and schemas — never
// their implementation.
func declarations() []*genai.FunctionDeclaration {
	all := tools()
	out := make([]*genai.FunctionDeclaration, 0, len(all))
	for _, t := range all {
		out = append(out, &genai.FunctionDeclaration{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Params,
		})
	}
	return out
}

// briefArgs renders a call's arguments for the progress line, values truncated
// so a long ticket body cannot push the interesting part off the screen.
func briefArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := argString(args, k)
		if len(v) > 48 {
			v = v[:45] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s=%q", k, v))
	}
	return strings.Join(parts, ", ")
}
