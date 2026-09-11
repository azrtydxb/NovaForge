package identity_test

import (
	"testing"

	"github.com/novaforge/novaforge/internal/identity"
)

func mustHash(t *testing.T, plain string) string {
	t.Helper()
	hash, err := identity.HashPassword(plain)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	return hash
}

func TestHashVerifyRoundTrip(t *testing.T) {
	hash := mustHash(t, "correct horse")
	if !identity.VerifyPassword(hash, "correct horse") {
		t.Fatal("want VerifyPassword true for correct password")
	}
	if identity.VerifyPassword(hash, "wrong") {
		t.Fatal("want VerifyPassword false for wrong password")
	}
}

func TestHashIsSalted(t *testing.T) {
	a := mustHash(t, "correct horse")
	b := mustHash(t, "correct horse")
	if a == b {
		t.Fatal("want two hashes of the same input to differ")
	}
}
