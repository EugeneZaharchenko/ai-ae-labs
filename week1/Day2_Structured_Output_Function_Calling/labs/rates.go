// Week 1 Part 2 — Structured Output & Function Calling.
//
// This file is the ADK-free core: currency-rate lookup with a real HTTP
// provider, input validation, and typed errors. It has no dependency on the
// agent framework at all, which is the point — the logic a tool wraps should be
// testable without a model, a key, or a network.
//
// The tool contract itself lives in agent.go.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dimetron/ai-eng-course/labs/week1/internal/ratecache"
)

// Sentinel errors. These matter more than they look: the text of a tool error
// goes back to the model as an observation, and the model decides whether to
// retry with corrected arguments or give up. A vague error ("request failed")
// produces a vague retry; a specific one ("unknown currency code XYZ") lets the
// model fix its own call. That is the week 4 self-correction path in miniature.
var (
	// ErrUnknownCurrency reports a syntactically valid but unsupported code.
	ErrUnknownCurrency = errors.New("unknown currency code")
	// ErrInvalidCode reports a malformed code (not three letters).
	ErrInvalidCode = errors.New("invalid currency code")
	// ErrUpstream reports that the rate provider failed.
	ErrUpstream = errors.New("rate provider unavailable")
)

// RateInput is the tool's input contract.
//
// The `jsonschema` tag value is a DESCRIPTION, not a constraint list. This is
// the single most common mistake with ADK Go tool schemas: writing
// `jsonschema:"required,enum=UAH"` produces a tool whose description is the
// literal string "required,enum=UAH", and an empty `jsonschema:""` is a hard
// error in jsonschema-go. Constraints belong in an explicit
// functiontool.Config.InputSchema, not in the tag.
type RateInput struct {
	Base   string `json:"base" jsonschema:"ISO 4217 code of the base currency, e.g. USD"`
	Target string `json:"target" jsonschema:"ISO 4217 code of the target currency, e.g. UAH"`
}

// RateOutput is the tool's output contract.
//
// Source is deliberately included: an agent answer that cannot say where a
// number came from is not auditable, and provenance is a week 3 theme that
// starts here.
type RateOutput struct {
	Base   string  `json:"base"`
	Target string  `json:"target"`
	Rate   float64 `json:"rate" jsonschema:"How many units of target one unit of base buys"`
	AsOf   string  `json:"as_of" jsonschema:"Rate date, YYYY-MM-DD"`
	Source string  `json:"source" jsonschema:"Provenance of the rate, e.g. nbu or fixture"`
}

// Provider fetches rates against UAH, which is the axis the National Bank of
// Ukraine publishes. Keeping this an interface is what lets the tests run
// offline and the binary run against the real bank.
type Provider interface {
	// Name is the provenance label the tool stamps into RateOutput.Source.
	// An agent answer that cannot say where a number came from is not
	// auditable, so every provider must identify itself.
	Name() string
	// RatesToUAH returns how many UAH one unit of each listed currency buys,
	// plus the date the rates are valid for.
	RatesToUAH(ctx context.Context) (map[string]float64, string, error)
}

// normalizeCode upper-cases and validates a currency code.
func normalizeCode(code string) (string, error) {
	c := strings.ToUpper(strings.TrimSpace(code))
	if len(c) != 3 {
		return "", fmt.Errorf("%w: %q must be three letters (ISO 4217)", ErrInvalidCode, code)
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return "", fmt.Errorf("%w: %q must be three letters (ISO 4217)", ErrInvalidCode, code)
		}
	}
	return c, nil
}

// Convert computes the base→target rate by cross-rating through UAH.
//
// UAH itself is treated as having rate 1.0, so UAH→EUR and EUR→UAH both work
// without the provider needing to publish a UAH row.
func Convert(ctx context.Context, p Provider, in RateInput) (RateOutput, error) {
	base, err := normalizeCode(in.Base)
	if err != nil {
		return RateOutput{}, err
	}
	target, err := normalizeCode(in.Target)
	if err != nil {
		return RateOutput{}, err
	}

	rates, asOf, err := p.RatesToUAH(ctx)
	if err != nil {
		return RateOutput{}, fmt.Errorf("%w: %s", ErrUpstream, err)
	}

	baseUAH, err := rateToUAH(base, rates)
	if err != nil {
		return RateOutput{}, err
	}
	targetUAH, err := rateToUAH(target, rates)
	if err != nil {
		return RateOutput{}, err
	}

	return RateOutput{
		Base:   base,
		Target: target,
		Rate:   baseUAH / targetUAH,
		AsOf:   asOf,
		Source: p.Name(),
	}, nil
}

// rateToUAH resolves one currency's UAH value, treating UAH as the unit.
func rateToUAH(code string, rates map[string]float64) (float64, error) {
	if code == "UAH" {
		return 1, nil
	}
	rate, ok := rates[code]
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrUnknownCurrency, code)
	}
	if rate <= 0 {
		// A zero or negative rate would produce +Inf or a negative price. Fail
		// loudly rather than hand the model a nonsense number it will happily
		// present as fact.
		return 0, fmt.Errorf("%w: %s has non-positive rate %v", ErrUpstream, code, rate)
	}
	return rate, nil
}

// NBUProvider reads the National Bank of Ukraine's public rate directory.
//
// Chosen deliberately for this course: it needs no API key, so the production
// path runs for every learner on day one (§2b "no surprise prerequisites"),
// and it is a real endpoint with real failure modes rather than a toy.
//
// HTTPClient defaults to a client wrapped in CachedTransport: rate lists
// change at most a few times a day, and a console session that asks three
// questions must not send three upstream requests. Set HTTPClient yourself
// (as tests do) to bypass or reconfigure the cache.
type NBUProvider struct {
	// BaseURL defaults to the NBU statistics service. Tests point it at an
	// httptest server.
	BaseURL string
	// HTTPClient defaults to a client with a timeout AND a 1-minute cache:
	// never use http.DefaultClient for outbound calls in production — it has
	// no timeout, so one hung upstream leaks a goroutine per request forever.
	HTTPClient *http.Client
}

const defaultNBUBaseURL = "https://bank.gov.ua/NBUStatService/v1/statdirectory/exchange"

// Name implements Provider.
func (p *NBUProvider) Name() string { return "nbu" }

// nbuRow is one row of the NBU response.
type nbuRow struct {
	Rate         float64 `json:"rate"`
	CC           string  `json:"cc"`
	ExchangeDate string  `json:"exchangedate"` // DD.MM.YYYY
}

// defaultRateClient returns the shared default HTTP client for live rate
// providers: a timeout PLUS the cached/limited transport (1-min TTL,
// 1-min interval). One client per process means the agent's tool calls and
// the compare tool's fetches share one cache — two calls to the same URL
// within a minute cost one upstream request.
func defaultRateHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: ratecache.NewCachedTransport(),
	}
}

// RatesToUAH implements Provider.
func (p *NBUProvider) RatesToUAH(ctx context.Context) (map[string]float64, string, error) {
	base := p.BaseURL
	if base == "" {
		base = defaultNBUBaseURL
	}
	client := p.HTTPClient
	if client == nil {
		client = defaultRateHTTPClient()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?json", nil)
	if err != nil {
		return nil, "", fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("get rates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected status %s", resp.Status)
	}

	var rows []nbuRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, "", fmt.Errorf("decode rates: %w", err)
	}
	if len(rows) == 0 {
		return nil, "", errors.New("empty rate directory")
	}

	out := make(map[string]float64, len(rows))
	for _, row := range rows {
		out[strings.ToUpper(row.CC)] = row.Rate
	}
	return out, isoDate(rows[0].ExchangeDate), nil
}

// isoDate converts NBU's DD.MM.YYYY to YYYY-MM-DD, leaving anything
// unparseable untouched rather than inventing a date.
func isoDate(ddmmyyyy string) string {
	t, err := time.Parse("02.01.2006", ddmmyyyy)
	if err != nil {
		return ddmmyyyy
	}
	return t.Format("2006-01-02")
}

// FixtureProvider is an offline Provider for tests and key-free demos.
type FixtureProvider struct {
	Rates map[string]float64
	Date  string
	Err   error
}

// Name implements Provider.
func (p *FixtureProvider) Name() string { return "fixture" }

// RatesToUAH implements Provider.
func (p *FixtureProvider) RatesToUAH(context.Context) (map[string]float64, string, error) {
	if p.Err != nil {
		return nil, "", p.Err
	}
	return p.Rates, p.Date, nil
}

// MonoProvider reads monobank's public currency rates. Like the NBU it needs
// no API key, so the live path runs for every learner; unlike the NBU it
// publishes bank buy/sell spreads instead of official directory rates.
//
// Two things make this a different KIND of upstream, worth teaching next to
// the NBU rather than instead of it:
//
//   - Rate limits are a published contract, not a rumour: /bank/currency may
//     be polled at most once per 5 minutes (per the OpenAPI description,
//     v250818). A caching layer belongs in front of it, not a retry loop.
//   - Its public surface is JSON with no schema document at that endpoint
//     (the docs page inlines an OpenAPI 3.0.3 spec client-side), so the
//     response contract lives in the struct below, exactly like
//     nbuRow above.
type MonoProvider struct {
	// BaseURL defaults to monobank's public API. Tests point it at an
	// httptest server.
	BaseURL string
	// HTTPClient defaults to defaultRateHTTPClient(): a timeout plus the
	// shared 1-minute cached/limited transport. Monobank's published limit is
	// one call per 5 minutes per /bank/currency — the limiter is the floor,
	// the cache is the reason a session usually never needs the second call.
	HTTPClient *http.Client
}

const defaultMonoBaseURL = "https://api.monobank.ua"

// iso4217Numeric maps the ISO 4217 numeric codes monobank publishes instead
// of three-letter codes. UAH is 980. The map is deliberately small and
// hand-checked; any code missing from it is SKIPPED, not guessed — an unknown
// pair then surfaces as ErrUnknownCurrency at the Convert boundary, exactly
// like an unknown NBU row.
var iso4217Numeric = map[int]string{
	980: "UAH", 840: "USD", 978: "EUR", 826: "GBP", 392: "JPY",
	756: "CHF", 156: "CNY", 784: "AED", 985: "PLN", 124: "CAD",
	36: "AUD", 203: "CZK", 208: "DKK", 348: "HUF", 410: "KRW",
	578: "NOK", 702: "SGD", 792: "TRY", 860: "UZS", 933: "BYN",
	944: "AZN", 971: "AFN", 8: "ALL", 51: "AMD", 32: "ARS",
	643: "RUB", 498: "MDL", 941: "RSD", 975: "BGN", 352: "ISK",
	986: "BRL", 554: "NZD", 682: "SAR", 704: "VND", 780: "TND",
}

// monoRow is one row of monobank's /bank/currency response. currencyCodeA/B
// are ISO 4217 numeric codes, and exactly one of rateBuy/rateSell/rateCross
// is set per row.
type monoRow struct {
	CurrencyCodeA int     `json:"currencyCodeA"`
	CurrencyCodeB int     `json:"currencyCodeB"`
	Date          int64   `json:"date"`
	RateBuy       float64 `json:"rateBuy"`
	RateSell      float64 `json:"rateSell"`
	RateCross     float64 `json:"rateCross"`
}

// Name implements Provider.
func (p *MonoProvider) Name() string { return "monobank" }

// RatesToUAH implements Provider. Only pairs with B=UAH (980) are usable for
// our cross-rating axis; other rows (e.g. EUR/USD) are skipped rather than
// guessed. rateBuy and rateSell are averaged into one rate, the same number a
// human sees as the midpoint; rateCross is used as-is.
func (p *MonoProvider) RatesToUAH(ctx context.Context) (map[string]float64, string, error) {
	rows, err := p.RatesToUAHDetailed(ctx)
	if err != nil {
		return nil, "", err
	}
	out := make(map[string]float64, len(rows))
	for _, r := range rows {
		out[r.CC] = r.Rate
	}
	// The freshest date wins: rows are already filtered to usable UAH pairs.
	var freshest time.Time
	for _, r := range rows {
		if t, err := time.Parse("2006-01-02", r.Date); err == nil && t.After(freshest) {
			freshest = t
		}
	}
	if freshest.IsZero() {
		return nil, "", errors.New("no UAH pairs in rate list")
	}
	return out, freshest.Format("2006-01-02"), nil
}

// MonoQuote is one UAH pair from monobank, kept at the buy/sell level.
type MonoQuote struct {
	CC    string  `json:"cc"`
	Buy   float64 `json:"buy"`   // 0 when monobank publishes only a cross rate
	Sell  float64 `json:"sell"`  // 0 when cross
	Cross float64 `json:"cross"` // 0 when buy/sell
	Rate  float64 `json:"rate"`  // midpoint of buy/sell, or the cross itself
	Date  string  `json:"date"`  // ISO date of this row
}

// RatesToUAHDetailed is RatesToUAH without collapsing buy/sell into one
// number: the compare tool needs the spread, not just the midpoint. Same
// filtering rules (UAH pairs, known ISO codes, positive rates).
func (p *MonoProvider) RatesToUAHDetailed(ctx context.Context) ([]MonoQuote, error) {
	base := p.BaseURL
	if base == "" {
		base = defaultMonoBaseURL
	}
	client := p.HTTPClient
	if client == nil {
		client = defaultRateHTTPClient()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/bank/currency", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get rates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}

	var rows []monoRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode rates: %w", err)
	}
	if len(rows) == 0 {
		return nil, errors.New("empty rate list")
	}

	var out []MonoQuote
	for _, row := range rows {
		if row.CurrencyCodeB != 980 { // UAH only
			continue
		}
		code, ok := iso4217Numeric[row.CurrencyCodeA]
		if !ok {
			continue
		}
		date := time.Unix(row.Date, 0).UTC().Format("2006-01-02")
		switch {
		case row.RateBuy > 0 && row.RateSell > 0:
			out = append(out, MonoQuote{
				CC:   code,
				Buy:  row.RateBuy,
				Sell: row.RateSell,
				Rate: (row.RateBuy + row.RateSell) / 2,
				Date: date,
			})
		case row.RateCross > 0:
			out = append(out, MonoQuote{
				CC:    code,
				Cross: row.RateCross,
				Rate:  row.RateCross,
				Date:  date,
			})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no UAH pairs in rate list")
	}
	return out, nil
}
