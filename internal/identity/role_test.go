package identity_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/identity"
)

// TestResolvedSubjectCarriesOrgRole pins what an owner-only action depends
// on. A subject said who the caller was and which organization they were
// verified in, but not what they could do there — so git-platform, which may
// not read the identity schema, had no way to tell an owner deleting a
// repository from any member doing the same.
func TestResolvedSubjectCarriesOrgRole(t *testing.T) {
	srv := newGRPCServer(t)
	ctx := context.Background()
	store := newStore(t)
	tokens := identity.NewTokenStore(storePool(t))

	owner := mustUser(t, store, "role-owner")
	member := mustUser(t, store, "role-member")
	orgName := "roles-" + uuid.NewString()[:8]
	org, err := store.CreateOrg(ctx, orgName, owner)
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if err := store.AddOrgMember(ctx, org.ID, member, "member"); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}

	for _, tc := range []struct {
		user uuid.UUID
		want string
	}{
		{owner, "owner"},
		{member, "member"},
	} {
		plaintext, _, err := tokens.Create(ctx, tc.user, "role", []string{"repo:read"}, nil)
		if err != nil {
			t.Fatalf("create token: %v", err)
		}
		resp, err := srv.ResolveToken(ctx, &identityv1.ResolveTokenRequest{Token: plaintext, Org: orgName})
		if err != nil {
			t.Fatalf("ResolveToken: %v", err)
		}
		if got := resp.GetSubject().GetRole(); got != tc.want {
			t.Fatalf("role = %q, want %q", got, tc.want)
		}

		// Without an organization there is no membership, so no role.
		bare, err := srv.ResolveToken(ctx, &identityv1.ResolveTokenRequest{Token: plaintext})
		if err != nil {
			t.Fatalf("ResolveToken without org: %v", err)
		}
		if got := bare.GetSubject().GetRole(); got != "" {
			t.Fatalf("role without an organization = %q, want empty", got)
		}
	}
}
