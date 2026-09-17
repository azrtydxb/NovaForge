package maintenance_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func checkGraphRPCScope(t *testing.T, client graphv1.GraphServiceClient, org, repo uuid.UUID, files []*gitv1.FileChange) {
	t.Helper()
	req := &graphv1.MaintenanceSnapshotRequest{RepoId: repo.String(), GoFileHashes: map[string]string{}}
	for _, file := range files {
		if strings.HasSuffix(file.GetPath(), ".go") {
			req.GoFileHashes[file.GetPath()] = graph.SourceDigest(file.GetContent())
		}
		if file.GetPath() == "go.mod" {
			req.ModuleHash = graph.SourceDigest(file.GetContent())
		}
	}
	if _, err := client.MaintenanceSnapshot(context.Background(), req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("anonymous graph evidence: %v", err)
	}
	incoming := func(org uuid.UUID) context.Context {
		tok, err := svcauth.Mint(sweepSecret, "graph-scope", org, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		// No outgoing metadata: this is the work-reviews handler's context.
		return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+tok))
	}
	if _, err := client.MaintenanceSnapshot(incoming(org), req); err != nil {
		t.Fatalf("incoming credential was not forwarded: %v", err)
	}
	if _, err := client.MaintenanceSnapshot(incoming(uuid.New()), req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("foreign organization read graph evidence: %v", err)
	}
	req.RepoId = uuid.NewString()
	if _, err := client.MaintenanceSnapshot(incoming(org), req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("foreign repository read graph evidence: %v", err)
	}
}
