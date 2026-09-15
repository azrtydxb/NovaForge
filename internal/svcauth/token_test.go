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

// TestAgentRunTokenNamesItsAgent pins what an agent run's credential must say
// for a capability grant to be applied to it. A grant is held by an agent id;
// a token that named only the organization gave every transport a caller with
// no id to look grants up for, so an agent's push was refused inside its own
// grant and nothing could tell that apart from a push outside it.
func TestAgentRunTokenNamesItsAgent(t *testing.T) {
	org, agent := uuid.New(), uuid.New()
	tok, err := svcauth.MintAgentRun("s3cret", org, agent, time.Minute)
	if err != nil {
		t.Fatalf("MintAgentRun: %v", err)
	}
	scope, err := svcauth.ScopeFromToken("s3cret", tok)
	if err != nil {
		t.Fatalf("ScopeFromToken: %v", err)
	}
	if scope.OrgID != org || scope.ActorID != agent || scope.ActorKind != "agent" {
		t.Fatalf("want agent %s in org %s, got %+v", agent, org, scope)
	}
}

// TestPlatformTokenNamesNoActor: a platform worker's token is the platform,
// and must not be mistaken for an agent that grants apply to.
func TestPlatformTokenNamesNoActor(t *testing.T) {
	org := uuid.New()
	tok, _ := svcauth.Mint("s3cret", "ci-scheduler", org, time.Minute)
	scope, err := svcauth.ScopeFromToken("s3cret", tok)
	if err != nil {
		t.Fatalf("ScopeFromToken: %v", err)
	}
	if scope.OrgID != org || scope.ActorID != uuid.Nil || scope.ActorKind != "service" {
		t.Fatalf("want a service scope with no actor, got %+v", scope)
	}
}

func TestAgentRunTokenWithoutAgentRefused(t *testing.T) {
	if _, err := svcauth.MintAgentRun("s3cret", uuid.New(), uuid.Nil, time.Minute); err == nil {
		t.Fatal("an agent run credential naming no agent must not be minted")
	}
}

func TestNoSecretRefuses(t *testing.T) {
	if _, err := svcauth.Mint("", "x", uuid.New(), time.Minute); err == nil {
		t.Fatal("minting without a secret must fail rather than produce an unsigned token")
	}
}
