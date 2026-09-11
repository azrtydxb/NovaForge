package identity_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/identity"
)

// TestTOTPEnrolmentIsTwoStep pins the property that makes enrolment safe: the
// secret is not written to the user row until the caller proves they can
// produce a code from it. Writing it at setup time would mark two-factor
// enabled and lock the user out of their own account if they never finished.
func TestTOTPEnrolmentIsTwoStep(t *testing.T) {
	srv := newGRPCServer(t)
	st := identity.NewStore(storePool(t))
	ctx := context.Background()

	userID := mustUser(t, st, "totp")
	authed := authz.WithScope(ctx, authz.Scope{ActorID: userID, ActorKind: "user"})

	setup, err := srv.Setup2FA(authed, &identityv1.Setup2FARequest{})
	if err != nil {
		t.Fatalf("Setup2FA: %v", err)
	}
	if setup.GetSecret() == "" {
		t.Fatal("no secret returned")
	}

	// Not enabled yet.
	u, err := st.UserByID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if u.TOTPSecret.Valid && u.TOTPSecret.String != "" {
		t.Fatal("setup must not enable two-factor before the code is verified")
	}

	// A wrong code does not enable it either.
	if _, err := srv.Verify2FA(authed, &identityv1.Verify2FARequest{Code: "000000"}); err == nil {
		t.Fatal("a wrong code must not complete enrolment")
	}

	code := identity.TOTPCode(setup.GetSecret())
	if _, err := srv.Verify2FA(authed, &identityv1.Verify2FARequest{Code: code}); err != nil {
		t.Fatalf("Verify2FA with a valid code: %v", err)
	}
	u, err = st.UserByID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if !u.TOTPSecret.Valid || u.TOTPSecret.String == "" {
		t.Fatal("verification must enable two-factor")
	}
}

func TestUserRPCsRequireAuthentication(t *testing.T) {
	srv := newGRPCServer(t)
	ctx := context.Background()
	if _, err := srv.GetCurrentUser(ctx, &identityv1.GetCurrentUserRequest{}); err == nil {
		t.Fatal("GetCurrentUser must refuse an unauthenticated caller")
	}
	if _, err := srv.ListSSHKeys(ctx, &identityv1.ListSSHKeysRequest{}); err == nil {
		t.Fatal("ListSSHKeys must refuse an unauthenticated caller")
	}
}

func TestDeleteTokenRefusesAnotherUsersToken(t *testing.T) {
	srv := newGRPCServer(t)
	st := identity.NewStore(storePool(t))
	ctx := context.Background()

	owner := mustUser(t, st, "owner")
	other := mustUser(t, st, "other")

	ownerCtx := authz.WithScope(ctx, authz.Scope{ActorID: owner, ActorKind: "user"})
	created, err := srv.CreateToken(ownerCtx, &identityv1.CreateTokenRequest{Name: "ci"})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	otherCtx := authz.WithScope(ctx, authz.Scope{ActorID: other, ActorKind: "user"})
	_, err = srv.DeleteToken(otherCtx, &identityv1.DeleteTokenRequest{Id: created.GetToken().GetId()})
	if err == nil {
		t.Fatal("a user must not revoke another user's token")
	}
	if !strings.Contains(err.Error(), "no such token") {
		t.Fatalf("want a not-found style refusal, got %v", err)
	}
	if _, err := uuid.Parse(created.GetToken().GetId()); err != nil {
		t.Fatalf("token id is not a uuid: %v", err)
	}
}
