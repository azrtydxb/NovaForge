package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/agentrun"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
)

func TestFinishResultRetriesRetainedAccounting(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := database.Migrate(url, "agents", agents.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := agents.NewStore(pool)
	org := uuid.New()
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorID: uuid.New(), ActorKind: "user"})
	agent, err := store.CreateAgent(ctx, agents.Agent{OrgID: org, Name: "test", Role: "engineer"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, agents.Run{OrgID: org, AgentID: agent.ID, SponsorID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetRunState(ctx, run.ID, "running"); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM agents.agent_runs WHERE id=$1 FOR UPDATE`, run.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	receipt := agents.Completion{State: "succeeded", Summary: "done", Spend: agents.Spend{Tokens: 73, TokensAvailable: true}}
	go func() {
		state, err := finishResult(ctx, store, nil, run, agentrun.Result{State: "succeeded", Completion: receipt})
		if err == nil && state != "succeeded" {
			t.Errorf("state %s", state)
		}
		done <- err
	}()
	time.Sleep(11 * time.Second) // first bounded attempt must actually time out
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetRun(ctx, run.ID)
	if err != nil || saved.TokensUsed != 73 || saved.State != "succeeded" || !saved.TokensAvailable {
		t.Fatalf("%+v %v", saved, err)
	}
}
