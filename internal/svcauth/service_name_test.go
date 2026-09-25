package svcauth_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/svcauth"
)

func TestServiceNamePreservedOnlyForVerifiedOrgService(t *testing.T) {
	const key = "service-name-test-key"
	org := uuid.New()
	token, err := svcauth.Mint(key, "ci-credentials", org, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := svcauth.ScopeFromToken(key, token)
	if err != nil || scope.ServiceName != "ci-credentials" || scope.ActorKind != "service" || scope.OrgID != org {
		t.Fatalf("verified service identity lost: %+v %v", scope, err)
	}
	token, err = svcauth.MintAgentRun(key, org, uuid.New(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	scope, err = svcauth.ScopeFromToken(key, token)
	if err != nil || scope.ServiceName != "" {
		t.Fatalf("agent became service: %+v %v", scope, err)
	}
	token, err = svcauth.MintPlatform(key, "ci-credentials", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svcauth.ScopeFromToken(key, token); err == nil {
		t.Fatal("unscoped platform worker became org service")
	}
}
