package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCredentialEchoRedaction(t *testing.T) {
	c := NewClient(ServerDef{}, nil)
	c.rememberCredential(`secret"one`)
	c.rememberCredential("rotated-two")
	raw := json.RawMessage(`{"secret\"one":{"text":"prefix secret\"one rotated-two","number":9007199254740993}}`)
	got, err := c.redactJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "secret") || strings.Contains(string(got), "rotated-two") || !strings.Contains(string(got), "9007199254740993") {
		t.Fatalf("unsafe or lossy redaction: %s", got)
	}
	if c.redactText("error rotated-two") != "error [REDACTED]" {
		t.Fatal("error echo retained")
	}
}
