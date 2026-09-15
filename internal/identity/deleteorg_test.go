package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/events"
)

// TestDeleteOrgIsAnOwnersDecision pins the organization deletion that had no
// API at all — an operator ran a script against the database. It is an
// owner's decision alone, made by naming the organization again; it is
// announced before anything is removed, so every service can remove that
// organization's data; and a deletion that cannot be announced does not
// happen.
func TestDeleteOrgIsAnOwnersDecision(t *testing.T) {
	srv := newGRPCServer(t)
	ctx := context.Background()
	register := func(prefix string) (uuid.UUID, string) {
		name := randomUsername(prefix)
		reg, err := srv.Register(ctx, &identityv1.RegisterRequest{Email: name + "@example.com", Username: name, Password: "correct horse battery staple"})
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		return uuid.MustParse(reg.GetUserId()), name
	}
	ownerID, _ := register("owner")
	adminID, adminName := register("admin")
	outsiderID, _ := register("outsider")
	owner := authz.WithScope(ctx, authz.Scope{ActorID: ownerID, ActorKind: "user"})
	admin := authz.WithScope(ctx, authz.Scope{ActorID: adminID, ActorKind: "user"})
	outsider := authz.WithScope(ctx, authz.Scope{ActorID: outsiderID, ActorKind: "user"})

	orgName := "doomed-" + uuid.NewString()[:8]
	created, err := srv.CreateOrg(owner, &identityv1.CreateOrgRequest{Name: orgName})
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if _, err := srv.AddOrgMember(owner, &identityv1.AddOrgMemberRequest{OrgId: orgName, Username: adminName, Role: "admin"}); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}

	var announced []events.OrgDeletedEvent
	srv.SetOrgDeletedPublisher(func(_ context.Context, e events.OrgDeletedEvent) error {
		announced = append(announced, e)
		return nil
	})

	for _, c := range []struct {
		who     string
		ctx     context.Context
		confirm string
		want    codes.Code
	}{
		{"an admin", admin, orgName, codes.PermissionDenied},
		{"a non-member", outsider, orgName, codes.PermissionDenied},
		{"an owner naming it wrongly", owner, orgName + "x", codes.InvalidArgument},
		{"an owner not naming it", owner, "", codes.InvalidArgument},
	} {
		if _, err := srv.DeleteOrg(c.ctx, &identityv1.DeleteOrgRequest{Org: orgName, ConfirmName: c.confirm}); status.Code(err) != c.want {
			t.Fatalf("%s: code = %v, want %v", c.who, status.Code(err), c.want)
		}
	}
	if len(announced) != 0 {
		t.Fatalf("a refused deletion was announced: %+v", announced)
	}

	srv.SetOrgDeletedPublisher(func(context.Context, events.OrgDeletedEvent) error { return errors.New("redis is down") })
	if _, err := srv.DeleteOrg(owner, &identityv1.DeleteOrgRequest{Org: orgName, ConfirmName: orgName}); status.Code(err) != codes.Unavailable {
		t.Fatalf("unannounceable deletion: code = %v, want Unavailable", status.Code(err))
	}
	if _, err := srv.GetOrg(owner, &identityv1.GetOrgRequest{Org: orgName}); err != nil {
		t.Fatalf("an unannounced deletion removed the organization: %v", err)
	}

	srv.SetOrgDeletedPublisher(func(_ context.Context, e events.OrgDeletedEvent) error {
		announced = append(announced, e)
		return nil
	})
	if _, err := srv.DeleteOrg(owner, &identityv1.DeleteOrgRequest{Org: orgName, ConfirmName: orgName}); err != nil {
		t.Fatalf("owner DeleteOrg: %v", err)
	}
	if len(announced) != 1 || announced[0].OrgID.String() != created.GetOrg().GetId() || announced[0].OrgName != orgName {
		t.Fatalf("announced = %+v, want one event for %s", announced, created.GetOrg().GetId())
	}
	if _, err := srv.GetOrg(owner, &identityv1.GetOrgRequest{Org: orgName}); err == nil {
		t.Fatal("the organization still resolves after its owner deleted it")
	}
	// Accounts are people's, not the organization's: they outlive it.
	if _, err := srv.GetCurrentUser(owner, &identityv1.GetCurrentUserRequest{}); err != nil {
		t.Fatalf("the owner's account went with the organization: %v", err)
	}
}
