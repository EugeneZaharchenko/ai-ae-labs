package main

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

// cacheCheck is the first node: a hit goes straight to the cached answer,
// a miss sends the query on to classify.
func (r *retriever) cacheCheck(ctx agent.Context, query string) (*session.Event, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("порожній запит")
	}
	answer, kind, err := r.cache.lookup(ctx, r.emb, r.scope, query)
	if err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	ev := session.NewEvent(ctx, ctx.InvocationID())
	if kind != "" {
		ev.Routes = []string{"hit"}
		ev.Output = answer
		return ev, nil
	}
	ev.Routes = []string{"miss"}
	ev.Output = query
	return ev, nil
}

func cachedAnswer(_ agent.Context, answer string) (string, error) {
	return answer, nil
}

// signerRow is one line of the multi-hop answer with the edges that prove it.
type signerRow struct {
	merchant         string
	tariff, contract *Edge
	signer           *Edge
}

// signerRows joins the facts of all branches into Merchant → Contract → Signer.
func signerRows(facts []Edge) []signerRow {
	var rows []signerRow
	find := func(from, rel string) *Edge {
		for i := range facts {
			if facts[i].From == from && facts[i].Rel == rel {
				return &facts[i]
			}
		}
		return nil
	}
	var merchants []string
	for _, f := range facts {
		if f.Rel == "has_contract" && !slices.Contains(merchants, f.From) {
			merchants = append(merchants, f.From)
		}
	}
	slices.Sort(merchants)
	for _, m := range merchants {
		row := signerRow{merchant: m, tariff: find(m, "on_tariff"), contract: find(m, "has_contract")}
		if row.contract != nil {
			row.signer = find(row.contract.To, "signed_by")
		}
		rows = append(rows, row)
	}
	return rows
}

func cite(s Span) string {
	return fmt.Sprintf("%s, %s, span %d:%d", s.ChunkID, s.DocumentID, s.Start, s.End)
}

// format writes the grounded answer with citations and stores it in the cache.
func (r *retriever) format(ctx agent.Context, ev Evidence) (string, error) {
	var b strings.Builder
	b.WriteString("[cache_hit=false] ")
	switch {
	case ev.Plan.Traverse:
		r.formatMultiHop(&b, ev)
	case ev.Plan.UseFilter:
		formatFilter(&b, ev)
	default:
		top := ev.Top[0]
		fmt.Fprintf(&b, "Топ-результат (%s, %s): %s", top.ChunkID, top.DocumentID, top.Text)
		if len(ev.Top) > 1 {
			b.WriteString("\nІнші докази:")
			for _, c := range ev.Top[1:] {
				fmt.Fprintf(&b, " %s (%s);", c.ChunkID, c.DocumentID)
			}
		}
	}
	fmt.Fprintf(&b, "\n[plan=%s steps=%d/%d]", ev.Plan.Class, ev.Steps, ev.Plan.Budget)
	answer := b.String()
	if err := r.cache.store(ctx, r.emb, r.scope, ev.Plan.Query, strings.Replace(answer, "[cache_hit=false]", "[cache_hit=true]", 1)); err != nil {
		return "", fmt.Errorf("cache: %w", err)
	}
	return answer, nil
}

func (r *retriever) formatMultiHop(b *strings.Builder, ev Evidence) {
	rows := signerRows(ev.Facts)
	var names []string
	for _, row := range rows {
		names = append(names, row.merchant)
	}
	tariffs := strings.Join(ev.Plan.Filter.Tariffs, ", ")
	if tariffs == "" && len(rows) > 0 && rows[0].tariff != nil {
		tariffs = rows[0].tariff.To
	}
	fmt.Fprintf(b, "1) Мерчанти на тарифі %s", tariffs)
	if ev.Plan.Filter.NBU {
		b.WriteString(", які підпадають під вимогу НБУ 2026")
	}
	fmt.Fprintf(b, ": %s.\n", strings.Join(names, ", "))
	for _, f := range ev.Facts {
		if f.Rel == "subject_to" {
			fmt.Fprintf(b, "   - %s підпадає під вимогу НБУ 2026 [%s] «%s»\n", f.From, cite(f.Source), r.graph.quote(f.Source))
			break
		}
	}
	for _, row := range rows {
		if row.tariff != nil {
			fmt.Fprintf(b, "   - %s на тарифі %s [%s]\n", row.merchant, row.tariff.To, cite(row.tariff.Source))
		}
	}
	b.WriteString("2) Хто підписував договори:\n")
	for _, row := range rows {
		fmt.Fprintf(b, "   - %s → договір %s [%s] → %s [%s]\n", row.merchant, row.contract.To,
			cite(row.contract.Source), row.signer.To, cite(row.signer.Source))
	}
}

func formatFilter(b *strings.Builder, ev Evidence) {
	covered := map[string]Edge{}
	for _, f := range ev.Facts {
		if f.Rel == "subject_to" {
			covered[f.From] = f
		}
	}
	b.WriteString("За фільтром:")
	seen := map[string]bool{}
	for _, f := range ev.Facts {
		if f.Rel != "on_tariff" || seen[f.From] {
			continue
		}
		seen[f.From] = true
		fmt.Fprintf(b, "\n   - мерчант %s на тарифі %s [%s]", f.From, f.To, cite(f.Source))
		if ev.Plan.Filter.NBU {
			if nbu, ok := covered[f.To]; ok {
				fmt.Fprintf(b, "; підпадає під вимогу НБУ 2026 [%s]", cite(nbu.Source))
			} else {
				b.WriteString("; НЕ підпадає під вимогу НБУ 2026")
				for _, nbu := range covered {
					fmt.Fprintf(b, " (вона поширюється на тариф %s [%s])", nbu.From, cite(nbu.Source))
				}
			}
		}
	}
}

// insufficient is the terminal node when the evidence does not pass the
// criterion or the budget ran out. Its answers are not cached.
func insufficient(_ agent.Context, ev Evidence) (string, error) {
	top := "немає кандидатів"
	if len(ev.Top) > 0 {
		top = fmt.Sprintf("топ-1 %s, score %.2f < gate %.2f", ev.Top[0].ChunkID, ev.Top[0].Score, qualityGate)
	}
	reason := top
	if ev.Truncated || ev.Steps > ev.Plan.Budget {
		reason = "вичерпано budget retrieval-кроків"
	} else if ev.Plan.Traverse {
		reason = "ланцюжок Merchant → Contract → Signer неповний"
	}
	return fmt.Sprintf("[cache_hit=false] Недостатньо доказів: %s. [plan=%s steps=%d/%d]",
		reason, ev.Plan.Class, ev.Steps, ev.Plan.Budget), nil
}
