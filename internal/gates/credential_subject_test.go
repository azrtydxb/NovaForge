package gates_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gates"
	"github.com/novaforge/novaforge/internal/secrets"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIssueLeaseRequiresOwnGrantSubject(t *testing.T) {
	url := dbURL(t)
	if err := database.Migrate(url, "secrets", secrets.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(url, "gitplatform", capability.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	org, actor := uuid.New(), uuid.New()
	store := capability.NewStore(pool)
	grant, err := store.Issue(context.Background(), capability.Grant{OrgID: org, SubjectID: actor, SubjectKind: "user", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM gitplatform.capability_grants WHERE id=$1 AND org_id=$2", grant.ID, org)
	})
	server := gates.NewGRPCServer(nil, nil, secrets.NewBroker(pool, []byte("subject-test-kek")), store)
	request := &gatesv1.IssueLeaseRequest{RunId: uuid.NewString(), GrantId: grant.ID.String(), Name: "TOKEN", TtlSeconds: 60}
	for name, scope := range map[string]authz.Scope{
		"other subject":     {OrgID: org, ActorID: uuid.New(), ActorKind: "user", Role: "owner"},
		"wrong kind":        {OrgID: org, ActorID: actor, ActorKind: "agent"},
		"zero actor":        {OrgID: org, ActorKind: "user"},
		"foreign org":       {OrgID: uuid.New(), ActorID: actor, ActorKind: "user"},
		"service borrowing": {OrgID: org, ActorID: actor, ActorKind: "service"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := server.IssueLease(authz.WithScope(context.Background(), scope), request)
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("subject guard: %v", err)
			}
		})
	}
	if _, err := server.IssueLease(context.Background(), request); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("missing scope: %v", err)
	}
	own := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorID: actor, ActorKind: "user"})
	// Authorized subject reaches the provider prerequisite, never another
	// subject's material. No provider exists here and no lease may be persisted.
	if _, err := server.IssueLease(own, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("own subject refused before provider prerequisite: %v", err)
	}
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM secrets.secret_leases WHERE org_id=$1", org).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unauthorized request persisted issuance: %d %v", count, err)
	}
}

func TestCredentialNilDependenciesFailClosed(t *testing.T) {
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: uuid.New(), ActorID: uuid.New(), ActorKind: "user"})
	server := gates.NewGRPCServer(nil, nil, nil, nil)
	if _, err := server.IssueLease(ctx, &gatesv1.IssueLeaseRequest{RunId: uuid.NewString(), GrantId: uuid.NewString(), Name: "TOKEN"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("nil issuer dependency: %v", err)
	}
	if _, err := server.RedeemLease(ctx, &gatesv1.RedeemLeaseRequest{RunId: uuid.NewString(), Token: "untrusted"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("nil redeemer dependency: %v", err)
	}
}
