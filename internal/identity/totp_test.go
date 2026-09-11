package identity

import (
	"testing"
	"time"
)

func TestTOTPKnownVector(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // gitleaks:allow -- RFC 6238 published test vector, not a real credential
	at := time.Unix(59, 0)

	if ValidateTOTP(secret, "no-such-code", at) {
		t.Fatal("want ValidateTOTP false for a bogus code")
	}

	code, err := generateTOTPCode(secret, at)
	if err != nil {
		t.Fatalf("generateTOTPCode: %v", err)
	}
	if !ValidateTOTP(secret, code, at) {
		t.Fatalf("want code %q generated for T=%v to validate at that instant", code, at)
	}
}

func TestTOTPWindowTolerance(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // gitleaks:allow -- RFC 6238 published test vector, not a real credential
	at := time.Unix(1_700_000_000, 0)

	code, err := generateTOTPCode(secret, at)
	if err != nil {
		t.Fatalf("generateTOTPCode: %v", err)
	}
	if !ValidateTOTP(secret, code, at.Add(29*time.Second)) {
		t.Fatal("want code to validate 29s later (within one 30s step)")
	}
	if ValidateTOTP(secret, code, at.Add(120*time.Second)) {
		t.Fatal("want code to fail 120s later (outside the tolerance window)")
	}
}
