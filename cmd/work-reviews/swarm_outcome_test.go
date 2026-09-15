package main

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/novaforge/novaforge/internal/work"
)

// TestSwarmReadsRunOutcomeFromAgentRuntime is the seam behind
// TestSwarmDependencyOrder: the scheduler's RunOutcome as work-reviews wires
// it, calling the real agent-runtime gRPC server over a real connection with
// the scheduler's own service token, answered from real run rows. A failed
// run is reported failed; a later run still going on the same subtask wins
// over it.
func TestSwarmReadsRunOutcomeFromAgentRuntime(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; source hack/env.sh")
	}
	if err := database.Migrate(url, "agents", agents.MigrationsFS); err != nil {
		t.Fatalf("migrate agents: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	const secret = "swarm-outcome-test"

	store := agents.NewStore(pool)
	gs := grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(nil, secret)))
	agentsv1.RegisterAgentServiceServer(gs, agents.NewGRPCServer(store, nil, nil, nil, nil))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = gs.Serve(l) }()
	defer gs.Stop()
	conn, err := grpc.NewClient(l.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	orgID := uuid.New()
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: orgID, ActorID: uuid.New(), ActorKind: "user"})
	agent, err := store.CreateAgent(ctx, agents.Agent{OrgID: orgID, Name: "fe-" + uuid.NewString()[:8], Role: "frontend", ModelRef: "m", Enabled: true})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	subtask := work.Item{ID: uuid.New(), Key: "NF-4"}
	newRun := func(state string) {
		t.Helper()
		run, err := store.CreateRun(ctx, agents.Run{OrgID: orgID, AgentID: agent.ID, WorkItemID: subtask.ID,
			SponsorID: uuid.New(), GrantID: uuid.New(), Branch: "agents/NF-4/work", WallclockLimit: time.Hour})
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if state == "running" || state == "over_budget" {
			if err := store.SetRunState(ctx, run.ID, "running"); err != nil {
				t.Fatalf("running: %v", err)
			}
		}
		if state != "running" {
			if err := store.SetRunState(ctx, run.ID, state); err != nil {
				t.Fatalf("%s: %v", state, err)
			}
		}
	}

	callCtx, err := withServiceIdentity(context.Background(), secret, orgID)
	if err != nil {
		t.Fatalf("service identity: %v", err)
	}
	outcome := agentRunOutcome(agentsv1.NewAgentServiceClient(conn), callCtx)

	if got, err := outcome(ctx, subtask); err != nil || got != "" {
		t.Fatalf("outcome with no run = %q, %v; want none", got, err)
	}
	newRun("over_budget")
	if got, err := outcome(ctx, subtask); err != nil || got != "over_budget" {
		t.Fatalf("outcome after the run went over budget = %q, %v; want over_budget", got, err)
	}
	newRun("running")
	if got, err := outcome(ctx, subtask); err != nil || got != "running" {
		t.Fatalf("outcome with a later run still going = %q, %v; want running", got, err)
	}
}
