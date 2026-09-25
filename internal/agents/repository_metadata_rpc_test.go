package agents_test

import (
	"context"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// repositoryMetadataPool migrates only a newly owned database. The shared test
// database is a connection endpoint, never a migration or cleanup target.
func repositoryMetadataPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	u, err := url.Parse(dbURL(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Close(context.Background()) })
	name := "agents_repo_test_" + uuid.NewString()
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := owner.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatalf("create owned repository metadata database: %v", err)
	}
	t.Logf("created owned database %s", name)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := owner.Exec(ctx, "DROP DATABASE "+quoted); err != nil {
			t.Errorf("drop owned database %s: %v", name, err)
		} else {
			t.Logf("dropped owned database %s", name)
		}
	})
	u.Path, u.RawPath = "/"+name, ""
	if err := database.Migrate(u.String(), "agents", agents.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// The real owner must serialize the repository read from its own persisted run.
// A controlled AgentService response would hide this seam from downstream
// consumers that require an exact repository match before authorizing an action.
func TestRepositoryMetadataAuthenticatedOwnerRPC(t *testing.T) {
	store := agents.NewStore(repositoryMetadataPool(t))
	org, foreignOrg := uuid.New(), uuid.New()
	ctx := scopedCtx(org)
	agent := mustCreateAgent(t, store, ctx, org)
	var runs []agents.Run
	for i := 0; i < 2; i++ {
		run, err := store.CreateRun(ctx, agents.Run{
			OrgID: org, RepoID: uuid.New(), AgentID: agent.ID,
			WorkItemID: uuid.New(), SponsorID: uuid.New(),
			Branch: "agents/repository-metadata/work", WallclockLimit: time.Hour,
		})
		if err != nil {
			t.Fatal(err)
		}
		saved, err := store.GetRun(ctx, run.ID)
		if err != nil || saved.RepoID != run.RepoID {
			t.Fatalf("persisted repository mismatch: got %s, want %s: %v", saved.RepoID, run.RepoID, err)
		}
		runs = append(runs, saved)
	}

	secret := uuid.NewString()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(nil, secret)))
	agentsv1.RegisterAgentServiceServer(server, agents.NewGRPCServer(store, nil, nil, nil, nil))
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	client := agentsv1.NewAgentServiceClient(conn)
	callCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	authenticated := func(orgID uuid.UUID) context.Context {
		token, err := svcauth.Mint(secret, "gates", orgID, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		// An asserted org header must not override the signed token's org.
		return metadata.NewOutgoingContext(callCtx, metadata.Pairs(
			"authorization", "Bearer "+token, "x-novaforge-org", org.String()))
	}
	ownerCtx, foreignCtx := authenticated(org), authenticated(foreignOrg)
	assertRepository := func(t *testing.T, got *agentsv1.Run, want agents.Run) {
		t.Helper()
		if got.GetId() != want.ID.String() || got.GetOrgId() != org.String() || got.GetRepoId() != want.RepoID.String() {
			t.Fatalf("owner RPC metadata: id=%q org=%q repo_id=%q; want id=%q org=%q persisted repo_id=%q",
				got.GetId(), got.GetOrgId(), got.GetRepoId(), want.ID, org, want.RepoID)
		}
	}
	t.Run("GetRun", func(t *testing.T) {
		for _, run := range runs {
			got, err := client.GetRun(ownerCtx, &agentsv1.GetRunRequest{Id: run.ID.String()})
			if err != nil {
				t.Fatal(err)
			}
			assertRepository(t, got.GetRun(), run)
		}
	})
	t.Run("ListRunsForWorkItem", func(t *testing.T) {
		for _, run := range runs {
			got, err := client.ListRunsForWorkItem(ownerCtx, &agentsv1.ListRunsForWorkItemRequest{WorkItemId: run.WorkItemID.String()})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.GetRuns()) != 1 || len(got.GetRunIds()) != 1 || got.GetRunIds()[0] != run.ID.String() {
				t.Fatalf("unexpected run listing: %v", got)
			}
			assertRepository(t, got.GetRuns()[0], run)
		}
	})
	t.Run("ForeignOrganization", func(t *testing.T) {
		for _, run := range runs {
			if _, err := client.GetRun(foreignCtx, &agentsv1.GetRunRequest{Id: run.ID.String()}); status.Code(err) != codes.NotFound {
				t.Fatalf("foreign GetRun: %v", err)
			}
			got, err := client.ListRunsForWorkItem(foreignCtx, &agentsv1.ListRunsForWorkItemRequest{WorkItemId: run.WorkItemID.String()})
			if err != nil || len(got.GetRuns()) != 0 || len(got.GetRunIds()) != 0 {
				t.Fatalf("foreign listing disclosed runs: %v, %v", got, err)
			}
		}
	})
	t.Run("Unauthenticated", func(t *testing.T) {
		if _, err := client.GetRun(callCtx, &agentsv1.GetRunRequest{Id: runs[0].ID.String()}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("unauthenticated GetRun: %v", err)
		}
	})
}
