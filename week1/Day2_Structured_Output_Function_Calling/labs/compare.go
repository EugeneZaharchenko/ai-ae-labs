// compare.go — the side-by-side NBU vs monobank rate table.
//
// It is deliberately NOT part of the agent: the compare tool is a plain CLI
// utility that reuses the same two Providers the agent uses, which is the
// lab's own lesson — the tool contract changes nothing when you swap the
// consumer.
//
// Run it:
//
//	go run . compare              # both live APIs
//	go run . compare -json        # also emit the table as JSON to stdout
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// CompareRow is one currency seen by both (or either) source.
// A nil pointer means "this source does not quote the currency".
type CompareRow struct {
	CC       string   `json:"cc"`
	NBU      *float64 `json:"nbu_official,omitempty"`
	MonoBuy  *float64 `json:"monobank_buy,omitempty"`
	MonoSell *float64 `json:"monobank_sell,omitempty"`
	MonoMid  *float64 `json:"monobank_mid,omitempty"`
	Spread   *float64 `json:"spread_pct,omitempty"`
	DeltaMid *float64 `json:"delta_mid_vs_nbu,omitempty"`
}

// Compare fetches both live lists and merges them by currency code. It also
// returns both sources' dates for the table header; because both providers
// go through the shared cached transport, Compare costs at most one upstream
// request per source per minute, no matter how many times it is called.
func Compare(ctx context.Context) ([]CompareRow, string, string, error) {
	return CompareWith(ctx, &NBUProvider{}, &MonoProvider{})
}

// CompareWith merges two explicit providers. Compare is this, wired to the two
// live ones.
//
// The seam exists so tests can point both sources at httptest servers and still
// exercise THIS merge — the real one. The first version of compare_test.go
// re-implemented the merge instead, which meant a bug here would have passed
// every test while the printed table stayed wrong. Duplicating logic in a test
// does not test the logic.
func CompareWith(ctx context.Context, nbu *NBUProvider, mono *MonoProvider) ([]CompareRow, string, string, error) {
	nbuRates, nbuDate, err := nbu.RatesToUAH(ctx)
	if err != nil {
		return nil, "", "", fmt.Errorf("nbu: %w", err)
	}
	monoQuotes, err := mono.RatesToUAHDetailed(ctx)
	if err != nil {
		return nil, "", "", fmt.Errorf("monobank: %w", err)
	}
	var monoDate string
	for _, q := range monoQuotes {
		if q.Date > monoDate {
			monoDate = q.Date
		}
	}

	rows := map[string]*CompareRow{}
	row := func(cc string) *CompareRow {
		r := rows[cc]
		if r == nil {
			r = &CompareRow{CC: cc}
			rows[cc] = r
		}
		return r
	}
	for cc, rate := range nbuRates {
		v := rate
		row(cc).NBU = &v
	}
	for _, q := range monoQuotes {
		r := row(q.CC)
		if q.Buy > 0 {
			buy, sell, mid := q.Buy, q.Sell, q.Rate
			r.MonoBuy, r.MonoSell, r.MonoMid = &buy, &sell, &mid
			if mid > 0 {
				sp := (sell - buy) / mid * 100
				r.Spread = &sp
			}
		} else {
			cross := q.Cross
			r.MonoMid = &cross
		}
	}

	var out []CompareRow
	for _, r := range rows {
		if r.NBU != nil && r.MonoMid != nil {
			d := *r.MonoMid - *r.NBU
			r.DeltaMid = &d
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		prio := map[string]int{"USD": 0, "EUR": 1, "PLN": 2, "GBP": 3}
		if prio[out[i].CC] != prio[out[j].CC] {
			return prio[out[i].CC] < prio[out[j].CC]
		}
		return out[i].CC < out[j].CC
	})
	return out, nbuDate, monoDate, nil
}

// CompareTable prints the human-readable side-by-side table and, when jsonOut
// is true, also writes the same rows as formatted JSON to stdout.
func CompareTable(rows []CompareRow, nbuDate, monoDate string, jsonOut bool) {
	num := func(p *float64, w int) string {
		if p == nil {
			return fmt.Sprintf("%*s", w, "—")
		}
		return fmt.Sprintf("%*.*f", w, 4, *p)
	}
	pct := func(p *float64) string {
		if p == nil {
			return "     —"
		}
		return fmt.Sprintf("%5.2f%%", *p)
	}
	dlt := func(p *float64) string {
		if p == nil {
			return "        —"
		}
		return fmt.Sprintf("%+8.4f", *p)
	}

	fmt.Println("NBU (official) vs monobank (bank buy/sell) — rates to UAH")
	fmt.Println()
	fmt.Println("CC   | NBU official | mono buy  | mono sell | mono mid  | spread |  mid−NBU")
	fmt.Println("-----+--------------+-----------+-----------+-----------+--------+---------")
	for _, r := range rows {
		fmt.Printf("%-4s | %s | %s | %s | %s | %s | %s\n",
			r.CC, num(r.NBU, 12), num(r.MonoBuy, 9), num(r.MonoSell, 9),
			num(r.MonoMid, 9), pct(r.Spread), dlt(r.DeltaMid))
	}
	fmt.Println()
	var both, nbuOnly, monoOnly int
	for _, r := range rows {
		switch {
		case r.NBU != nil && r.MonoMid != nil:
			both++
		case r.NBU != nil:
			nbuOnly++
		default:
			monoOnly++
		}
	}
	fmt.Printf("rows: %d | both: %d | NBU-only: %d | monobank-only: %d | dates: NBU %s, monobank %s\n",
		len(rows), both, nbuOnly, monoOnly, nbuDate, monoDate)

	if jsonOut {
		b, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "json: %v\n", err)
			return
		}
		fmt.Println()
		fmt.Println(string(b))
	}
}
