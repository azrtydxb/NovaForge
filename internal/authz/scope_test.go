package authz_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
)

func TestRequireOrgRejectsCrossOrg(t *testing.T) {
	orgA, orgB, user := uuid.New(), uuid.New(), uuid.New()
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: orgA, ActorID: user, ActorKind: "user"})
	err := authz.RequireOrg(ctx, orgB)
	if err == nil {
		t.Fatal("want cross-org error")
	}
	if !strings.Contains(err.Error(), "cross-org") {
		t.Fatalf("want 'cross-org' in error, got %v", err)
	}
}

func TestRequireOrgAllowsSameOrg(t *testing.T) {
	orgA := uuid.New()
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: orgA, ActorID: uuid.New(), ActorKind: "user"})
	if err := authz.RequireOrg(ctx, orgA); err != nil {
		t.Fatalf("want nil for same org, got %v", err)
	}
}

func TestFromContextEmpty(t *testing.T) {
	if _, err := authz.FromContext(context.Background()); err == nil {
		t.Fatal("want error when no scope is installed")
	}
}

func TestRequireOrgWithoutScopeDenies(t *testing.T) {
	if err := authz.RequireOrg(context.Background(), uuid.New()); err == nil {
		t.Fatal("want error when no scope is installed")
	}
}
