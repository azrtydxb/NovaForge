package agents_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/agents"
)

func mustCreateRun(t *testing.T, store *agents.Store, ctx context.Context, orgID uuid.UUID) agents.Run {
	t.Helper()
	agent := mustCreateAgent(t, store, ctx, orgID)
	run, err := store.CreateRun(ctx, agents.Run{
		OrgID:           orgID,
		AgentID:         agent.ID,
		SponsorID:       uuid.New(),
		GrantID:         uuid.New(),
		Branch:          uniqueName("agents/NF-audit/run"),
		WallclockLimit:  time.Hour,
		TokenLimit:      1_000_000,
		CostLimitMicros: 5_000_000,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return run
}

func TestRecordThenComplete(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	run := mustCreateRun(t, store, ctx, orgID)

	audit := agents.NewAuditLog(store.Pool())
	id, err := audit.Record(ctx, agents.Entry{
		RunID:    run.ID,
		Tool:     "repo.read_file",
		ArgsJSON: []byte(`{"path":"cmd/main.go"}`),
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := audit.Complete(ctx, id, "ok", ""); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	entries, err := audit.List(ctx, run.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if entries[0].Tool != "repo.read_file" {
		t.Fatalf("want tool repo.read_file, got %q", entries[0].Tool)
	}
	if entries[0].Outcome != "ok" {
		t.Fatalf("want outcome ok, got %q", entries[0].Outcome)
	}
	if string(entries[0].ArgsJSON) == "" {
		t.Fatal("want args json, got empty")
	}
}

func TestArgsAreStoredVerbatim(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	run := mustCreateRun(t, store, ctx, orgID)

	audit := agents.NewAuditLog(store.Pool())
	args := []byte(`{"path":"cmd/main.go","ref":"main"}`)
	id, err := audit.Record(ctx, agents.Entry{
		RunID:    run.ID,
		Tool:     "repo.read_file",
		ArgsJSON: args,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := audit.Complete(ctx, id, "ok", ""); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	entries, err := audit.List(ctx, run.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}

	var want, got map[string]any
	if err := json.Unmarshal(args, &want); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	if err := json.Unmarshal(entries[0].ArgsJSON, &got); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if want["path"] != got["path"] || want["ref"] != got["ref"] {
		t.Fatalf("args did not round-trip: want %v, got %v", want, got)
	}
}

func TestFailedCallRetainsError(t *testing.T) {
	store := newStore(t)
	orgID := uuid.New()
	ctx := scopedCtx(orgID)
	run := mustCreateRun(t, store, ctx, orgID)

	audit := agents.NewAuditLog(store.Pool())
	id, err := audit.Record(ctx, agents.Entry{
		RunID:    run.ID,
		Tool:     "git.commit",
		ArgsJSON: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := audit.Complete(ctx, id, "error", "permission denied"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	entries, err := audit.List(ctx, run.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if entries[0].Outcome != "error" {
		t.Fatalf("want outcome error, got %q", entries[0].Outcome)
	}
	if entries[0].Error != "permission denied" {
		t.Fatalf("want error text permission denied, got %q", entries[0].Error)
	}
}
