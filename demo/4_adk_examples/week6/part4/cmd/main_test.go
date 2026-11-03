package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestRoundTripOverA2A(t *testing.T) {
	var out bytes.Buffer
	if code := runCLI(nil, &out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "ALLOWED") {
		t.Fatalf("remote answer missing:\n%s", out.String())
	}
}

func TestCallbackCanRefuse(t *testing.T) {
	var out bytes.Buffer
	if code := runCLI([]string{"-deny-callback"}, &out); code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "allowlist") {
		t.Fatalf("refusal must name the cause:\n%s", out.String())
	}
}

func TestAgentCardIsTheContract(t *testing.T) {
	base, stop, err := servePolicyAgent()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	resp, err := http.Get(base + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var card struct {
		Name   string `json:"name"`
		Skills []any  `json:"skills"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	if card.Name != "refund_policy" || len(card.Skills) == 0 {
		t.Fatalf("unexpected card: %+v", card)
	}
}
