// C2 · Hierarchical Decomposition — a tree of agents: each level splits its
// goal into sub-goals, delegates down, and synthesises the results up.
// agenttool.New turns an agent into a tool, so the parent keeps control of the
// synthesis and each child runs in its own session (context isolation).
//
//	lead ─→ research_manager ─→ market_analyst
//	     │                   └→ competitor_analyst
//	     └→ writing_manager  ─→ editor
//
//	go run .              # scripted demo, offline
//	go run . console
package main

import (
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/agenttool"

	"github.com/dimetron/ai-eng-course/labs/demo/3_adk2_patterns/internal/kit"
)

var spec = kit.Spec{
	ID:    "C2",
	Title: "Hierarchical Decomposition",
	Input: "Write a short competitive analysis of the EV charger market",
	Build: build,
}

func main() { kit.Main(spec) }

// delegate returns a brain that calls each child tool once, in order, with the
// same request, then synthesises their results with done.
func delegate(children []string, done func(results []string) kit.Reply) kit.Brain {
	return func(p kit.Prompt) kit.Reply {
		var results []string
		for _, c := range children {
			r, ok := p.Result(c)
			if !ok {
				return kit.Call(c, map[string]any{"request": p.Text})
			}
			results = append(results, fmt.Sprint(r["result"]))
		}
		return done(results)
	}
}

func leaf(text string) kit.Brain {
	return func(kit.Prompt) kit.Reply { return kit.Say("%s", text) }
}

// node builds one tree level: an LlmAgent whose tools are its children.
func node(m kit.Models, name, instruction string, brain kit.Brain, children ...agent.Agent) (agent.Agent, error) {
	tools := make([]tool.Tool, 0, len(children))
	for _, c := range children {
		tools = append(tools, agenttool.New(c, nil))
	}
	return llmagent.New(llmagent.Config{
		Name:        name,
		Description: instruction,
		Model:       m.For(name, brain),
		Instruction: instruction,
		Tools:       tools,
	})
}

func build(m kit.Models) (agent.Agent, error) {
	market, err := node(m, "market_analyst",
		"Estimate market size and growth for the topic in one sentence.",
		leaf("market: 2.1M public chargers in EU, +28%/yr"))
	if err != nil {
		return nil, err
	}
	competitors, err := node(m, "competitor_analyst",
		"Name the top competitors for the topic in one sentence.",
		leaf("competitors: Ionity, Tesla, Allego"))
	if err != nil {
		return nil, err
	}
	editor, err := node(m, "editor",
		"Turn the notes you receive into two polished sentences.",
		func(p kit.Prompt) kit.Reply { return kit.Say("Report: %s", p.Text) })
	if err != nil {
		return nil, err
	}

	research, err := node(m, "research_manager",
		"Split research into market and competitor questions, call market_analyst and competitor_analyst, merge their findings into one line.",
		delegate([]string{"market_analyst", "competitor_analyst"}, func(r []string) kit.Reply {
			return kit.Say("%s; %s", r[0], r[1])
		}), market, competitors)
	if err != nil {
		return nil, err
	}
	writing, err := node(m, "writing_manager",
		"Pass the research notes to editor and return the edited report.",
		delegate([]string{"editor"}, func(r []string) kit.Reply { return kit.Say("%s", r[0]) }),
		editor)
	if err != nil {
		return nil, err
	}

	// The lead calls research first, then hands the research RESULT (not the
	// raw user request) to writing — synthesis flows up, then down again.
	leadBrain := func(p kit.Prompt) kit.Reply {
		res, ok := p.Result("research_manager")
		if !ok {
			return kit.Call("research_manager", map[string]any{"request": p.Text})
		}
		doc, ok := p.Result("writing_manager")
		if !ok {
			return kit.Call("writing_manager", map[string]any{"request": fmt.Sprint(res["result"])})
		}
		return kit.Say("%v", doc["result"])
	}
	return node(m, "lead",
		"You lead a research task. Call research_manager with the goal, then writing_manager with the research result, then return the report.",
		leadBrain, research, writing)
}
