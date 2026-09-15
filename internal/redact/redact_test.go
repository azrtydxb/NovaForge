package redact_test

import (
	"testing"

	"github.com/novaforge/novaforge/internal/redact"
)

// TestLineMasksEveryValueWhole pins the two ways masking goes wrong: a value
// that contains another must be masked whole, not left with its tail showing,
// and a value too short to be a credential must not mangle every line.
func TestLineMasksEveryValueWhole(t *testing.T) {
	r := redact.New([]string{"abcd", "abcdefgh-long", "xy"})
	got := r.Line("a=abcdefgh-long b=abcd c=xy")
	if want := "a=*** b=*** c=xy"; got != want {
		t.Fatalf("Line = %q, want %q", got, want)
	}
	var none *redact.Redactor
	if none.Line("plain") != "plain" {
		t.Fatal("a nil redactor must pass lines through")
	}
}
