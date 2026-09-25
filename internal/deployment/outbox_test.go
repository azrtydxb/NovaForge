package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/redis/go-redis/v9"
	"os"
	"testing"
)

func TestSuccessfulDeliveryCreatesDurableEvidence(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	if _, err = s.Execute(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	// Replaying successful execution must not create another delivery event.
	if _, err = s.Execute(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	var raw []byte
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM deployment.success_outbox WHERE org_id=$1 AND operation_id=$2`, op.OrgID, op.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("want one durable event, got %d", count)
	}
	if err = s.pool.QueryRow(ctx, `SELECT payload FROM deployment.success_outbox WHERE org_id=$1 AND operation_id=$2`, op.OrgID, op.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"evidence_id": op.ID.String() + ":1", "org_id": op.OrgID.String(), "repo_id": op.RepoID.String(), "operation_id": op.ID.String(), "run_id": op.RunID.String(), "artifact": op.Artifact, "target": op.Target, "destination": op.Destination, "target_revision": op.TargetRevision, "external_id": op.ID.String(), "latest_execute_attempt": float64(1)} {
		if got[key] != want {
			t.Errorf("%s: got %v want %v", key, got[key], want)
		}
	}
	if _, present := got["commit"]; present {
		t.Fatal("unattested commit lineage")
	}
}

func eventPlatform() context.Context {
	return authz.WithScope(context.Background(), authz.Scope{ActorKind: "service", PlatformWorker: "deployment-events"})
}
func eventOrganization(org uuid.UUID) context.Context {
	return authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorKind: "service", ServiceName: "deployment-events"})
}

func TestSuccessOutboxRollbackAndRecoveryBindDeliveryAttempt(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	// Reject only this owned database's outbox writes, exercising the actual finish
	// transaction rather than assuming two independently successful writes are atomic.
	_, err = s.pool.Exec(ctx, `ALTER TABLE deployment.success_outbox ADD CONSTRAINT unavailable CHECK (false)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(ctx, op.ID); !errors.Is(err, ErrUncertain) {
		t.Fatalf("outbox outage: %v", err)
	}
	got, err := s.Get(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateRunning || got.Attempts[0].State != StateRunning {
		t.Fatal("success escaped rollback")
	}
	if _, err = s.pool.Exec(ctx, `ALTER TABLE deployment.success_outbox DROP CONSTRAINT unavailable`); err != nil {
		t.Fatal(err)
	}
	s = restartService(t, s, req)
	if _, err = s.Reconcile(admin, op.ID); err != nil {
		t.Fatal(err)
	}
	var event SuccessEvent
	if err = s.pool.QueryRow(ctx, `SELECT payload FROM deployment.success_outbox WHERE org_id=$1 AND operation_id=$2`, op.OrgID, op.ID).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if event.EvidenceID != op.ID.String()+":1" || event.LatestExecuteAttempt != 1 {
		t.Fatalf("recovery invented a delivery attempt: %+v", event)
	}
}

func TestSuccessOutboxFailureRetryAndPublicationRecovery(t *testing.T) {
	s, ctx, admin, req, executor := fixture(t)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	executor.fail = true
	if _, err = s.Execute(ctx, op.ID); err == nil {
		t.Fatal("expected failed fixture")
	}
	ids, err := s.claimSuccess(eventPlatform(), 100)
	if err != nil || len(ids) != 0 {
		t.Fatal("failed delivery emitted success", err)
	}
	executor.fail = false
	if _, err = s.Retry(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	ids, err = s.claimSuccess(eventPlatform(), 100)
	if err != nil || len(ids) != 1 || ids[0].attempt != 2 {
		t.Fatal("missing retry event", ids, err)
	}
	id := ids[0]
	for _, bad := range []context.Context{ctx, admin, eventOrganization(uuid.New()), eventPlatform()} {
		if err = s.publishSuccess(bad, id, func(context.Context, SuccessEvent) error { t.Error("unauthorized publication"); return nil }); err == nil {
			t.Fatal("publication accepted unauthorized scope")
		}
	}
	for _, bad := range []context.Context{ctx, admin, eventOrganization(op.OrgID), authz.WithScope(ctx, authz.Scope{ActorKind: "service", PlatformWorker: "other"})} {
		if _, err = s.claimSuccess(bad, 100); err == nil {
			t.Fatal("unauthorized inventory")
		}
	}
	serviceCtx := eventOrganization(op.OrgID)
	if err = s.publishSuccess(serviceCtx, id, func(context.Context, SuccessEvent) error { return errors.New("transport unavailable") }); err == nil {
		t.Fatal("ignored publication failure")
	}
	s = restartService(t, s, req)
	ids, err = s.claimSuccess(eventPlatform(), 100)
	if err != nil || len(ids) != 1 {
		t.Fatal("lost obligation across restart", err)
	}
	// Use only a random owned stream on real Redis, never production event keys.
	options, err := redis.ParseURL(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(options)
	defer rdb.Close()
	stream := "test:deployment:" + uuid.NewString()
	defer rdb.Del(context.Background(), stream)
	var observed []SuccessEvent
	publish := func(ctx context.Context, event SuccessEvent) error {
		observed = append(observed, event)
		if err := events.Publish(ctx, rdb, stream, event); err != nil {
			return err
		}
		if len(observed) == 1 {
			return errors.New("lost acknowledgement after durable XADD")
		}
		return nil
	}
	if err = s.publishSuccess(serviceCtx, id, publish); err == nil {
		t.Fatal("lost reply marked published")
	}
	if err = s.publishSuccess(serviceCtx, id, publish); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 2 || observed[0] != observed[1] || observed[0].EvidenceID != op.ID.String()+":2" {
		t.Fatal("retry changed evidence identity")
	}
	if err = s.publishSuccess(serviceCtx, id, publish); err != nil || len(observed) != 2 {
		t.Fatal("acknowledged event republished", err)
	}
	messages, err := rdb.XRange(context.Background(), stream, "-", "+").Result()
	if err != nil || len(messages) != 2 {
		t.Fatal("real Redis evidence missing", err)
	}
	ids, err = s.claimSuccess(eventPlatform(), 100)
	if err != nil || len(ids) != 0 {
		t.Fatal("acknowledged obligation rediscovered", err)
	}
}
