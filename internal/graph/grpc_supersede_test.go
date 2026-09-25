package graph_test

import (
	"github.com/google/uuid"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/knowledge"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestSupersedeKnowledgeOwnerRPC(t *testing.T) {
	pool := storePool(t)
	if err := database.Migrate(dbURL(t), "knowledge", knowledge.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	k := knowledge.NewStore(pool)
	org, repo := uuid.New(), uuid.New()
	ctx := authz.WithScope(t.Context(), authz.Scope{OrgID: org, ActorKind: "user", Role: "owner"})
	record := func() knowledge.Entry {
		e, err := k.Record(ctx, knowledge.Entry{OrgID: org, RepoID: repo, Key: uuid.NewString(), Kind: "correction", Title: "fix", Body: "evidence"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	a, b := record(), record()
	server := graph.NewGRPCServer(nil, nil, k, nil, nil, nil)
	req := &graphv1.SupersedeKnowledgeRequest{OldId: a.ID.String(), ReplacementId: b.ID.String()}
	for _, scope := range []authz.Scope{{OrgID: org, ActorKind: "user", Role: "member"}, {OrgID: org, ActorKind: "agent"}, {OrgID: org, ActorKind: "service"}, {ActorKind: "user", Role: "owner"}} {
		if _, err := server.SupersedeKnowledge(authz.WithScope(t.Context(), scope), req); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("scope %+v accepted: %v", scope, err)
		}
	}
	if _, err := server.SupersedeKnowledge(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := server.SupersedeKnowledge(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := server.SupersedeKnowledge(authz.WithScope(t.Context(), authz.Scope{OrgID: uuid.New(), ActorKind: "user", Role: "owner"}), req); status.Code(err) != codes.NotFound {
		t.Fatalf("foreign: %v", err)
	}
}
