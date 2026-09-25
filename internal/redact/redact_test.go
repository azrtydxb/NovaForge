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

func TestStructuredAndMultilineCredentials(t *testing.T) {
	for _, value := range []string{`{"username":"db-user","password":"pw","nested":{"token":"nested-sensitive"},"key":"first-sensitive-line\nsecond-sensitive-line"}`, "first-sensitive-line\nsecond-sensitive-line"} {
		r := redact.New([]string{value})
		for _, secret := range []string{"first-sensitive-line", "second-sensitive-line"} {
			if r.Line("error: "+secret) != "error: ***" {
				t.Errorf("multiline constituent leaked: %q", secret)
			}
		}
		if value[0] == '{' {
			for _, secret := range []string{"db-user", "pw", "nested-sensitive"} {
				if r.Line("error: "+secret) != "error: ***" {
					t.Errorf("JSON constituent leaked: %q", secret)
				}
			}
		}
	}
}

func TestKubeconfigAndPEMMultiline(t *testing.T) {
	r := redact.New([]string{"users:\n- name: fixture-user\n  user:\n    token: yaml-sensitive-token\n    client-key-data: base64-sensitive-key\n    key: |\n      -----BEGIN PRIVATE KEY-----\n      private-key-body\n      -----END PRIVATE KEY-----\n"})
	for _, secret := range []string{"yaml-sensitive-token", "base64-sensitive-key", "private-key-body"} {
		if r.Line("error: "+secret) != "error: ***" {
			t.Errorf("structured multiline constituent leaked")
		}
	}
}

// Valid JSON surrogate pairs in unrelated metadata must not disable masking
// of any credential field. JSON numbers must retain their original spelling.
func TestJSONEscapedUnicodeConstituents(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		secrets     []string
	}{
		{"metadata", `{"note":"\ud83d\ude00","password":"sensitive-password"}`, []string{"sensitive-password", "😀"}},
		{"short", `{"note":"\ud83d\ude00","password":"pw"}`, []string{"pw"}},
		{"nested-array", `{"note":"\ud83d\ude00","items":[{"password":"first-line\nsecond-line"},["\ud83d\udd11"]]}`, []string{"first-line", "second-line", "🔑"}},
		{"top-level-array", `["\ud83d\ude00",{"password":"array-sensitive"},true,false,null]`, []string{"😀", "array-sensitive", "true", "false", "null"}},
		{"duplicate-fields", `{"note":"\ud83d\ude00","nested":{"password":"first-sensitive","password":"second-sensitive"}}`, []string{"first-sensitive", "second-sensitive"}},
		{"numbers", `{"note":"\ud83d\ude00","values":[9007199254740993,1.2300e+45,-0,123456789012345678901234567890]}`, []string{"9007199254740993", "1.2300e+45", "-0", "123456789012345678901234567890"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := redact.New([]string{tc.value})
			if got := r.Line(tc.value); got != redact.Mask {
				t.Errorf("full raw credential not masked: %q", got)
			}
			for _, secret := range tc.secrets {
				if got := r.Line("credential=" + secret); got != "credential="+redact.Mask {
					t.Errorf("JSON constituent not masked: %q", got)
				}
			}
		})
	}
}

func TestJSONScalarKeepsOpaqueMinimumLength(t *testing.T) {
	r := redact.New([]string{`"pw"`, `12`})
	if got := r.Line(`"pw" pw 12`); got != `*** pw 12` {
		t.Fatalf("opaque scalar behavior changed: %q", got)
	}
}
