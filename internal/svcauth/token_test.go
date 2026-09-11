package svcauth_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/svcauth"
)

func TestMintVerifyRoundTrip(t *testing.T) {
	org := uuid.New()
	tok, err := svcauth.Mint("s3cret", "ci-scheduler", org, time.Minute)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	svc, gotOrg, err := svcauth.Verify("s3cret", tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if svc != "ci-scheduler" || gotOrg != org {
		t.Fatalf("claims not carried: %s %s", svc, gotOrg)
	}
}

func TestWrongSecretRejected(t *testing.T) {
	tok, _ := svcauth.Mint("right", "x", uuid.New(), time.Minute)
	if _, _, err := svcauth.Verify("wrong", tok); err == nil {
		t.Fatal("a token signed with another secret must be rejected")
	}
}

func TestTamperedPayloadRejected(t *testing.T) {
	tok, _ := svcauth.Mint("s", "x", uuid.New(), time.Minute)
	parts := strings.SplitN(strings.TrimPrefix(tok, svcauth.Prefix), ".", 2)
	forged := svcauth.Prefix + "eyJzdmMiOiJhdHRhY2tlciJ9." + parts[1]
	if _, _, err := svcauth.Verify("s", forged); err == nil {
		t.Fatal("a token whose payload was swapped must be rejected")
	}
}

func TestExpiredRejected(t *testing.T) {
	tok, _ := svcauth.Mint("s", "x", uuid.New(), -time.Second)
	if _, _, err := svcauth.Verify("s", tok); err == nil {
		t.Fatal("an expired token must be rejected")
	}
}

func TestUserCredentialIsNotAServiceToken(t *testing.T) {
	if _, _, err := svcauth.Verify("s", "nf_a_personal_access_token"); err == nil {
		t.Fatal("a user credential must not verify as a service token")
	}
}

func TestNoSecretRefuses(t *testing.T) {
	if _, err := svcauth.Mint("", "x", uuid.New(), time.Minute); err == nil {
		t.Fatal("minting without a secret must fail rather than produce an unsigned token")
	}
}
