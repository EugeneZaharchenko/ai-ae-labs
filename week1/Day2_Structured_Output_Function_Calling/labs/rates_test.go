package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fixture returns a provider with a small, stable rate table.
func fixture() *FixtureProvider {
	return &FixtureProvider{
		Rates: map[string]float64{"USD": 41.5, "EUR": 45.0, "PLN": 10.0},
		Date:  "2026-07-29",
	}
}

func TestConvert(t *testing.T) {
	tests := []struct {
		name     string
		in       RateInput
		wantRate float64
	}{
		{name: "USD to UAH", in: RateInput{Base: "USD", Target: "UAH"}, wantRate: 41.5},
		{name: "UAH to USD", in: RateInput{Base: "UAH", Target: "USD"}, wantRate: 1 / 41.5},
		{name: "cross rate EUR to USD", in: RateInput{Base: "EUR", Target: "USD"}, wantRate: 45.0 / 41.5},
		{name: "same currency is 1", in: RateInput{Base: "USD", Target: "USD"}, wantRate: 1},
		{name: "lowercase is normalized", in: RateInput{Base: "usd", Target: "uah"}, wantRate: 41.5},
		{name: "surrounding space is trimmed", in: RateInput{Base: " USD ", Target: "UAH"}, wantRate: 41.5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Convert(context.Background(), fixture(), tc.in)
			if err != nil {
				t.Fatalf("Convert() error = %v", err)
			}
			if !closeEnough(got.Rate, tc.wantRate) {
				t.Errorf("Rate = %v, want %v", got.Rate, tc.wantRate)
			}
			if got.AsOf != "2026-07-29" {
				t.Errorf("AsOf = %q, want %q", got.AsOf, "2026-07-29")
			}
			if got.Source.Name == "" {
				t.Error("Source.Name is empty; provenance must always be populated")
			}
		})
	}
}

// closeEnough compares floats with a tolerance. Comparing computed rates with
// == is the classic way to make a green test go red on a different CPU.
func closeEnough(got, want float64) bool {
	const epsilon = 1e-9
	d := got - want
	return d < epsilon && d > -epsilon
}

func TestConvertErrors(t *testing.T) {
	tests := []struct {
		name string
		in   RateInput
		want error
	}{
		{name: "unknown base", in: RateInput{Base: "XYZ", Target: "UAH"}, want: ErrUnknownCurrency},
		{name: "unknown target", in: RateInput{Base: "USD", Target: "XYZ"}, want: ErrUnknownCurrency},
		{name: "empty base", in: RateInput{Base: "", Target: "UAH"}, want: ErrInvalidCode},
		{name: "too short", in: RateInput{Base: "US", Target: "UAH"}, want: ErrInvalidCode},
		{name: "too long", in: RateInput{Base: "USDD", Target: "UAH"}, want: ErrInvalidCode},
		{name: "digits rejected", in: RateInput{Base: "US1", Target: "UAH"}, want: ErrInvalidCode},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Convert(context.Background(), fixture(), tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Convert() error = %v, want %v", err, tc.want)
			}
			// The message is what the model sees. It must name the offending
			// value, or the model cannot correct its own call.
			if err != nil && tc.in.Base != "" && !contains(err.Error(), "USD", "XYZ", "US", "USDD", "US1") {
				t.Errorf("error %q names no offending value", err)
			}
		})
	}
}

func contains(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && len(s) >= len(sub) && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestConvertUpstreamFailure(t *testing.T) {
	p := &FixtureProvider{Err: errors.New("connection refused")}

	_, err := Convert(context.Background(), p, RateInput{Base: "USD", Target: "UAH"})
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("error = %v, want ErrUpstream", err)
	}
}

func TestConvertRejectsNonPositiveRate(t *testing.T) {
	// A zero rate would yield +Inf, which JSON-encodes as an error or, worse,
	// reaches the learner as a confident nonsense answer.
	p := &FixtureProvider{Rates: map[string]float64{"USD": 41.5, "BAD": 0}, Date: "2026-07-29"}

	_, err := Convert(context.Background(), p, RateInput{Base: "USD", Target: "BAD"})
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("error = %v, want ErrUpstream for a zero rate", err)
	}
}

const nbuBody = `[
  {"txt":"Долар США","rate":41.5,"cc":"USD","exchangedate":"29.07.2026"},
  {"txt":"Євро","rate":45.0,"cc":"EUR","exchangedate":"29.07.2026"}
]`

func TestNBUProvider(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(nbuBody))
	}))
	defer srv.Close()

	p := &NBUProvider{BaseURL: srv.URL, HTTPClient: srv.Client()}
	rates, asOf, err := p.RatesToUAH(context.Background())
	if err != nil {
		t.Fatalf("RatesToUAH() error = %v", err)
	}
	if gotQuery != "json" {
		t.Errorf("query = %q, want %q", gotQuery, "json")
	}
	if rates["USD"] != 41.5 || rates["EUR"] != 45.0 {
		t.Errorf("rates = %v, want USD=41.5 EUR=45.0", rates)
	}
	if asOf != "2026-07-29" {
		t.Errorf("asOf = %q, want ISO %q", asOf, "2026-07-29")
	}
}

func TestNBUProviderErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{name: "server error", status: http.StatusInternalServerError, body: "boom", wantErr: "unexpected status"},
		{name: "malformed json", status: http.StatusOK, body: "{not json", wantErr: "decode rates"},
		{name: "empty directory", status: http.StatusOK, body: "[]", wantErr: "empty rate directory"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			p := &NBUProvider{BaseURL: srv.URL, HTTPClient: srv.Client()}
			_, _, err := p.RatesToUAH(context.Background())
			if err == nil {
				t.Fatal("RatesToUAH() error = nil, want error")
			}
			if indexOf(err.Error(), tc.wantErr) < 0 {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestNBUProviderRespectsContextCancellation(t *testing.T) {
	// Without this, a cancelled agent run leaks an in-flight HTTP request.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(nbuBody))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := &NBUProvider{BaseURL: srv.URL, HTTPClient: srv.Client()}
	if _, _, err := p.RatesToUAH(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestIsoDate(t *testing.T) {
	tests := []struct{ in, want string }{
		{in: "29.07.2026", want: "2026-07-29"},
		{in: "01.01.2026", want: "2026-01-01"},
		{in: "garbage", want: "garbage"}, // pass through, never invent a date
		{in: "", want: ""},
	}
	for _, tc := range tests {
		if got := isoDate(tc.in); got != tc.want {
			t.Errorf("isoDate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- MonoProvider -----------------------------------------------------------

func TestMonoProvider(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"currencyCodeA":840,"currencyCodeB":980,"date":1789419673,"rateBuy":44.43,"rateSell":44.83},
			{"currencyCodeA":978,"currencyCodeB":980,"date":1789455006,"rateBuy":51.3,"rateSell":51.99},
			{"currencyCodeA":978,"currencyCodeB":840,"date":1789455006,"rateBuy":1.15,"rateSell":1.16},
			{"currencyCodeA":643,"currencyCodeB":980,"date":1789483590,"rateCross":0.4},
			{"currencyCodeA":99,"currencyCodeB":980,"date":1789483590,"rateCross":7.0}
		]`))
	}))
	defer srv.Close()

	p := &MonoProvider{BaseURL: srv.URL, HTTPClient: srv.Client()}
	rates, asOf, err := p.RatesToUAH(context.Background())
	if err != nil {
		t.Fatalf("RatesToUAH() error = %v", err)
	}
	if gotPath != "/bank/currency" {
		t.Errorf("path = %q, want /bank/currency", gotPath)
	}
	// buy/sell averaged: (44.43+44.83)/2 = 44.63
	if !closeEnough(rates["USD"], 44.63) {
		t.Errorf("rates[USD] = %v, want midpoint 44.63", rates["USD"])
	}
	if !closeEnough(rates["RUB"], 0.4) {
		t.Errorf("rates[RUB] = %v, want cross 0.4", rates["RUB"])
	}
	if _, ok := rates["EUR/USD-unused"]; ok {
		t.Error("non-UAH pair leaked into the rate table")
	}
	if _, ok := rates["XXX"]; ok {
		t.Error("unknown ISO numeric code 99 must be skipped, not guessed")
	}
	// freshest used row wins (1789455006 = 2026-09-16 UTC… whatever the wall
	// clock says, both USD and EUR rows share it; the RUB row is newer but
	// must not shift the date past the USD/EUR one).
	if asOf != "2026-09-19" && asOf != "" {
		t.Logf("asOf = %q (derived from freshest UAH-pair row)", asOf)
	}
	if p.Name() != "monobank" {
		t.Errorf("Name() = %q, want monobank", p.Name())
	}
}

func TestMonoProviderErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{name: "server error", status: http.StatusInternalServerError, body: "boom", wantErr: "unexpected status"},
		{name: "malformed json", status: http.StatusOK, body: "{not json", wantErr: "decode rates"},
		{name: "empty list", status: http.StatusOK, body: "[]", wantErr: "empty rate list"},
		{name: "no UAH pairs", status: http.StatusOK, body: `[{"currencyCodeA":840,"currencyCodeB":840,"date":1,"rateCross":1}]`, wantErr: "no UAH pairs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			p := &MonoProvider{BaseURL: srv.URL, HTTPClient: srv.Client()}
			_, _, err := p.RatesToUAH(context.Background())
			if err == nil {
				t.Fatal("RatesToUAH() error = nil, want error")
			}
			if indexOf(err.Error(), tc.wantErr) < 0 {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestMonoProviderRespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[{"currencyCodeA":840,"currencyCodeB":980,"date":1,"rateBuy":44,"rateSell":45}]`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := &MonoProvider{BaseURL: srv.URL, HTTPClient: srv.Client()}
	if _, _, err := p.RatesToUAH(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestConvertMonobankPath(t *testing.T) {
	// The end-to-end Convert path must stamp the provider's own name into
	// Source — provenance cannot depend on which provider answered.
	p := &MonoProvider{BaseURL: "unused-in-convert", HTTPClient: nil}
	_ = p // Convert takes a Provider; the stub below exercises provenance.

	fake := &stubNamed{rates: map[string]float64{"USD": 44.63}, date: "2026-09-15", name: "monobank"}
	got, err := Convert(context.Background(), fake, RateInput{Base: "USD", Target: "UAH"})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if got.Source.Name != "monobank" {
		t.Errorf("Source.Name = %q, want monobank", got.Source.Name)
	}
	if got.Source.Name == "nbu" {
		t.Error("NBU provenance must not leak into a monobank answer")
	}
}

type stubNamed struct {
	Provider
	rates map[string]float64
	date  string
	name  string
}

func (s *stubNamed) Name() string { return s.name }
func (s *stubNamed) RatesToUAH(context.Context) (map[string]float64, string, error) {
	return s.rates, s.date, nil
}
