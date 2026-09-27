package main

import (
	"github.com/novaforge/novaforge/internal/agents"
	"testing"
)

func TestEffectivePriceUsesSelectedModel(t *testing.T) {
	prices := map[string]agents.TokenPrice{"default": {InputMicrosPerMillion: 1}, "override": {InputMicrosPerMillion: 99}}
	if got := effectivePrice(prices, "default", ""); got == nil || got.InputMicrosPerMillion != 1 {
		t.Fatal("default price mismatch")
	}
	if got := effectivePrice(prices, "default", "override"); got == nil || got.InputMicrosPerMillion != 99 {
		t.Fatal("selected price mismatch")
	}
	if effectivePrice(prices, "default", "unknown") != nil {
		t.Fatal("unknown model inherited default price")
	}
}
