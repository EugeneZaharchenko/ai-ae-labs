package react

import "context"

// RulePlanner is the offline Planner: a fixed three-step investigation with no
// model behind it. It exists so the loop can be run and tested with no key and
// no network — and it is labelled as such wherever it is used.
//
// The script walks through every shape of step the lab cares about: two
// independent reads in ONE step, then one side-effecting call, then the answer.
type RulePlanner struct{ step int }

// NextStep implements Planner.
func (p *RulePlanner) NextStep(_ context.Context, h History) (Step, error) {
	p.step++
	switch p.step {
	case 1:
		return Step{
			Thought: "Check the runbooks and the merchant payouts at the same time; the two reads do not depend on each other",
			Calls: []ToolCall{
				{ID: "c1", Name: "search_internal_wiki", Args: map[string]any{"query": "payout incident"}},
				{ID: "c2", Name: "get_merchant_payouts", Args: map[string]any{"merchant_id": "A-114", "since": "03:00"}},
			},
		}, nil
	case 2:
		return Step{
			Thought: "Enough evidence; file a ticket",
			Calls: []ToolCall{{ID: "c3", Name: "create_jira_ticket", Args: map[string]any{
				"title": "payout incident follow-up", "body": "see incident #418"}}},
		}, nil
	default:
		return Step{
			Thought: "Ticket created, reporting back",
			Final:   true,
			Answer:  "Related incident #418 found; acquirer says unrelated. " + lastResult(h),
		}, nil
	}
}

// lastResult returns the content of the newest successful observation.
func lastResult(h History) string {
	for i := len(h) - 1; i >= 0; i-- {
		for j := len(h[i].Results) - 1; j >= 0; j-- {
			if r := h[i].Results[j]; r.Err == nil {
				return "Ticket: " + r.Content
			}
		}
	}
	return ""
}
