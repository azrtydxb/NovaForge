package maintenance_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Incoming-only metadata simulates an on-demand work-reviews request. The
// periodic sweeper's outgoing token would mask a missing stream interceptor.
func assertBenchmarkCredentials(t *testing.T, client civ1.CIServiceClient, org, repo uuid.UUID) {
	t.Helper()
	incoming := func(org uuid.UUID) context.Context {
		tok, err := svcauth.Mint(sweepSecret, maintenanceService, org, svcauth.DefaultTTL)
		if err != nil {
			t.Fatal(err)
		}
		return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+tok))
	}
	ctx := incoming(org)
	runs, err := client.ListRuns(ctx, &civ1.ListRunsRequest{RepoId: repo.String()})
	if err != nil || len(runs.GetRuns()) == 0 {
		t.Fatalf("forwarded run query: %v", err)
	}
	arts, err := client.ListArtifacts(ctx, &civ1.ListArtifactsRequest{RunId: runs.GetRuns()[0].GetId()})
	if err != nil || len(arts.GetArtifacts()) == 0 {
		t.Fatalf("forwarded artifact query: %v", err)
	}
	request := &civ1.DownloadArtifactRequest{ArtifactId: arts.GetArtifacts()[0].GetId()}
	stream, err := client.DownloadArtifact(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := stream.Recv()
	if err != nil || !strings.Contains(string(chunk.GetData()), "BenchmarkAlloc") {
		t.Fatalf("streamed evidence did not forward incoming credentials: %v", err)
	}
	other, err := client.DownloadArtifact(incoming(uuid.New()), request)
	if err == nil {
		_, err = other.Recv()
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("another organization could read benchmark evidence: %v", err)
	}
}

const maintenanceService = "maintenance-test"
