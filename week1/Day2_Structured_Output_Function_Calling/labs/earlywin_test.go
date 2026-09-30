package main

import (
	"context"
	"encoding/json"
	"testing"
)

func TestEarlyWin(t *testing.T) {
	raw := []byte(`{"base": "US1", "target": "UAH"}`)

	var in RateInput
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatalf("JSON не розібрався: %v", err)
	}

	_, err := Convert(context.Background(), &FixtureProvider{
		Rates: map[string]float64{"USD": 41.5, "EUR": 45.0},
		Date:  "2026-07-29",
	}, in)
	if err == nil {
		t.Fatal("очікували помилку, а валідатор пропустив зіпсований виклик")
	}
	t.Logf("валідатор відхилив виклик: %v", err)
}
