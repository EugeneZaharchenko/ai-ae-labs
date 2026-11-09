package main

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
)

// printTrace prints one line per event: when, who, which branch, what.
// Parallel desks show up as interleaved lines on different branches.
func printTrace(w io.Writer, events []*session.Event) {
	for _, ev := range events {
		branch := ev.Branch
		if branch == "" {
			branch = "-"
		}
		for _, what := range describe(ev) {
			fmt.Fprintf(w, "%s  %-16s %-22s %s\n", ev.Timestamp.Format("15:04:05.000"), ev.Author, branch, what)
		}
	}
}

func describe(ev *session.Event) []string {
	var out []string
	if ev.Content != nil {
		for _, p := range ev.Content.Parts {
			switch {
			case p == nil:
			case p.FunctionCall != nil && p.FunctionCall.Name == workflow.WorkflowInputFunctionCallName:
				out = append(out, "ПАУЗА: запит людині "+p.FunctionCall.ID)
			case p.FunctionCall != nil:
				out = append(out, fmt.Sprintf("виклик %s(%v)", p.FunctionCall.Name, p.FunctionCall.Args))
			case p.FunctionResponse != nil && p.FunctionResponse.Name == workflow.WorkflowInputFunctionCallName:
				out = append(out, fmt.Sprintf("ВІДНОВЛЕННЯ: відповідь %v на %s", p.FunctionResponse.Response["payload"], p.FunctionResponse.ID))
			case p.FunctionResponse != nil:
				out = append(out, toolResult(p.FunctionResponse.Name, p.FunctionResponse.Response))
			case strings.TrimSpace(p.Text) != "":
				out = append(out, "текст: "+short(p.Text, 90))
			}
		}
	}
	if ev.Output != nil {
		if m, ok := asMap(ev.Output); ok {
			out = append(out, fmt.Sprintf("вихід %s: %s", nodeOf(m), summarize(m)))
		} else {
			out = append(out, "вихід: "+short(fmt.Sprint(ev.Output), 90))
		}
	}
	if len(out) == 0 && len(ev.Actions.StateDelta) > 0 {
		keys := make([]string, 0, len(ev.Actions.StateDelta))
		for k := range ev.Actions.StateDelta {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		out = append(out, "стан: "+strings.Join(keys, ", "))
	}
	return out
}

func toolResult(name string, resp map[string]any) string {
	if e, ok := resp["error"]; ok {
		return fmt.Sprintf("інструмент %s → ПОМИЛКА %v", name, e)
	}
	if f, ok := resp["findings"].([]any); ok {
		return fmt.Sprintf("інструмент %s → %d фактів", name, len(f))
	}
	return fmt.Sprintf("інструмент %s → %s", name, short(fmt.Sprint(resp), 60))
}

// nodeOf names a function node by the shape of its output: top-level static
// nodes carry no node path on their events.
func nodeOf(m map[string]any) string {
	has := func(k string) bool { _, ok := m[k]; return ok }
	switch {
	case has("sent"):
		return "notify_board"
	case has("interrupt_id") && has("draft"):
		return "approval_gate"
	case has("document"):
		return "draft"
	case has("desks"):
		return "plan"
	case has("sections"):
		return "merge"
	}
	if len(m) > 0 && !has("topic") {
		return "gather"
	}
	return "вузол"
}

func summarize(m map[string]any) string {
	switch nodeOf(m) {
	case "notify_board":
		return fmt.Sprintf("sent=%v %v", m["sent"], m["reason"])
	case "approval_gate":
		return fmt.Sprintf("status=%v by=%v", m["status"], m["by"])
	case "draft":
		return fmt.Sprintf("hash=%v", short(fmt.Sprint(m["hash"]), 20))
	case "plan":
		return fmt.Sprintf("desks=%v", m["desks"])
	case "merge":
		var parts []string
		if secs, ok := m["sections"].([]any); ok {
			for _, s := range secs {
				sm, _ := s.(map[string]any)
				st := "verified"
				if v, _ := sm["verified"].(bool); !v {
					st = fmt.Sprintf("GAP %v", sm["code"])
				}
				parts = append(parts, fmt.Sprintf("%v=%s", sm["desk"], st))
			}
		}
		return strings.Join(parts, " ")
	case "gather":
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		return "зібрано " + strings.Join(keys, ", ")
	}
	return short(fmt.Sprint(m), 90)
}

func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

type tokenRow struct {
	author                string
	calls                 int
	prompt, output, total int32
}

// printTokens sums UsageMetadata per author. No metadata is said out loud,
// not printed as zeros.
func printTokens(w io.Writer, events []*session.Event, offline bool) {
	rows := map[string]*tokenRow{}
	var order []string
	for _, ev := range events {
		u := ev.UsageMetadata
		if u == nil {
			continue
		}
		r, ok := rows[ev.Author]
		if !ok {
			r = &tokenRow{author: ev.Author}
			rows[ev.Author] = r
			order = append(order, ev.Author)
		}
		r.calls++
		r.prompt += u.PromptTokenCount
		r.output += u.CandidatesTokenCount
		r.total += u.TotalTokenCount
	}
	if len(order) == 0 {
		why := "модель не повернула UsageMetadata"
		if offline {
			why += " (офлайн-фейк не рахує токени)"
		}
		fmt.Fprintf(w, "токени: %s\n", why)
		return
	}
	fmt.Fprintln(w, "токени (UsageMetadata подій):")
	var sum tokenRow
	for _, a := range order {
		r := rows[a]
		fmt.Fprintf(w, "  %-16s викликів=%d prompt=%d output=%d total=%d\n", r.author, r.calls, r.prompt, r.output, r.total)
		sum.prompt, sum.output, sum.total = sum.prompt+r.prompt, sum.output+r.output, sum.total+r.total
	}
	fmt.Fprintf(w, "  %-16s prompt=%d output=%d total=%d\n", "РАЗОМ", sum.prompt, sum.output, sum.total)
}
