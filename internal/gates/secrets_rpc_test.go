package gates_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gates"
	"github.com/novaforge/novaforge/internal/secrets"
)

// TestSecretsAreWriteOnlyAndBrokeredOnlyToCI pins who may do what with a
// secret: only an owner or admin sets one, nothing that answers a person
// carries its value back, and a person or an agent cannot ask the broker for
// a job's credential — only the platform's CI service can.
func TestSecretsAreWriteOnlyAndBrokeredOnlyToCI(t *testing.T) {
	url := dbURL(t)
	if err := database.Migrate(url, "secrets", secrets.MigrationsFS); err != nil {
		t.Fatalf("migrate secrets: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	srv := gates.NewGRPCServer(nil, nil, secrets.NewBroker(pool, []byte("rpc-test-kek")), nil)

	org := uuid.New()
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM secrets.secret_values WHERE org_id = $1", org) })
	value := "nf-" + uuid.NewString()
	put := &gatesv1.PutSecretRequest{Name: "DEPLOY_TOKEN", Environment: "staging", Value: value}

	member := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorID: uuid.New(), ActorKind: "user", Role: "member"})
	if _, err := srv.PutSecret(member, put); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("member setting a secret: %v, want PermissionDenied", err)
	}
	resp, err := srv.PutSecret(adminCtx(org), put)
	if err != nil {
		t.Fatalf("admin setting a secret: %v", err)
	}
	list, err := srv.ListSecrets(adminCtx(org), &gatesv1.ListSecretsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{protojson.Format(resp), protojson.Format(list)} {
		if strings.Contains(msg, value) {
			t.Fatalf("a read of secrets carries the value: %s", msg)
		}
	}
	if _, err := srv.PutSecret(adminCtx(org), &gatesv1.PutSecretRequest{Name: "deploy-token", Environment: "staging", Value: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("a name that cannot be an environment variable: %v, want InvalidArgument", err)
	}

	ask := &gatesv1.IssueJobLeaseRequest{JobId: uuid.NewString(), RepoId: uuid.NewString(), Ref: "refs/heads/main", Name: "DEPLOY_TOKEN"}
	for _, kind := range []string{"user", "agent"} {
		ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorID: uuid.New(), ActorKind: kind, Role: "owner"})
		if _, err := srv.IssueJobLease(ctx, ask); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%s asking for a job credential: %v, want PermissionDenied", kind, err)
		}
	}
}
