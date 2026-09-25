package secrets_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/secrets"
)

func TestDeploymentCredentialIntentFencesLatePrepare(t *testing.T) {
	b, _, org, _ := dynamicBroker(t, time.Minute)
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorKind: "service", ServiceName: "deployment-credentials"})
	req := secrets.DeploymentCredentialRequest{OperationID: uuid.New(), AttemptID: uuid.New(), RepoID: uuid.New(), RunID: uuid.New(), Target: "owned", Environment: "staging", ProviderBinding: "owned-bao", Name: "API_KEY", ExpiresAt: time.Now().Add(time.Minute)}
	if _, err := b.PrepareDeployment(ctx, req); !errors.Is(err, secrets.ErrHardExpiryUnavailable) {
		t.Fatalf("unqualified engine accepted: %v", err)
	}
	mutated := req
	mutated.Name = "OTHER"
	if _, err := b.RevokeDeployment(ctx, mutated); err == nil {
		t.Fatal("mismatched intent cleanup allowed")
	}
	result, err := b.RevokeDeployment(ctx, req)
	if err != nil || !result.Fenced || result.Pending != 0 {
		t.Fatalf("cleanup %+v %v", result, err)
	}
	if _, err := b.PrepareDeployment(ctx, req); !errors.Is(err, secrets.ErrScopeClosed) {
		t.Fatalf("late Prepare escaped: %v", err)
	}
	// A fresh broker sees the same durable closure, including before issuance.
	fresh := secrets.NewBroker(brokerPool(t), []byte("key"))
	if _, err := fresh.PrepareDeployment(ctx, req); !errors.Is(err, secrets.ErrScopeClosed) {
		t.Fatal("restart lost closure")
	}
	req.AttemptID = uuid.New()
	cleanupCtx := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorKind: "service", ServiceName: "deployment-cleanup"})
	if _, err := fresh.RevokeDeployment(cleanupCtx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.PrepareDeployment(ctx, req); !errors.Is(err, secrets.ErrScopeClosed) {
		t.Fatal("absent intent cancel did not fence")
	}
	if _, err := fresh.PrepareDeployment(cleanupCtx, req); err == nil {
		t.Fatal("cleanup identity issued")
	}
	if _, err := fresh.RevokeDeployment(context.Background(), req); err == nil {
		t.Fatal("anonymous cleanup")
	}
}
