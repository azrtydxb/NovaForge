package deployment

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
)

func TestDeploymentCleanupDiscoverySurvivesAuthorAndFairlyRetries(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	credentials := &partialCredentials{prepareErr: ErrUncertain, revokeErr: errors.New("issuer unavailable")}
	helm, _ := recoveryHelm(t, credentials)
	bindRecovery(t, s, req, helm)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	if _, err = s.Execute(ctx, op.ID); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	// A second pending obligation stands behind a permanently failing first one.
	// Both are deployment-owned rows; no foreign schema fixture is fabricated.
	_, err = s.pool.Exec(ctx, `INSERT INTO deployment.attempts(org_id,operation_id,number,kind,actor_id,actor_kind,state) VALUES($1,$2,2,'observe',$3,'agent','uncertain');`, op.OrgID, op.ID, op.ActorID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO deployment.credential_obligations(org_id,operation_id,attempt,provider_binding) VALUES($1,$2,2,$3)`, op.OrgID, op.ID, helm.Revision())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []context.Context{ctx, admin, janitorContext(ctx), authz.WithScope(ctx, authz.Scope{ActorKind: "service", PlatformWorker: "other"})} {
		if _, err = s.claimCleanup(bad, 1); err == nil {
			t.Fatal("unauthorized discovery")
		}
	}
	platform := authz.WithScope(context.Background(), authz.Scope{ActorKind: "service", PlatformWorker: "deployment-cleanup"})
	first, err := s.claimCleanup(platform, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.claimCleanup(platform, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || first[0].attempt == second[0].attempt {
		t.Fatal("pending first attempt starved later obligation")
	}
	// No resolver may be reached after author expiry/deletion; janitor uses only
	// persisted obligations and its own org-scoped credential.
	s.resolveRun = func(context.Context, uuid.UUID) (Run, error) {
		t.Error("cleanup consulted expired/deleted author")
		return Run{}, errors.New("author gone")
	}
	if err = s.CleanupPending(platform, uuid.NewString(), 100); err == nil {
		t.Fatal("provider outage acknowledged")
	}
	assertPending(t, s, ctx, op.ID, 2)
	credentials.revokeErr = nil
	if err = s.CleanupPending(platform, uuid.NewString(), 100); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got.Credentials {
		if c.ResolvedAt == nil {
			t.Fatal("confirmed cleanup not durable")
		}
	}
	if got.State != StateUncertain {
		t.Fatal("janitor invented deployment success")
	}
	restarted := restartService(t, s, req)
	remaining, err := restarted.claimCleanup(platform, 100)
	if err != nil || len(remaining) != 0 {
		t.Fatal("resolved cleanup rediscovered", err)
	}
}
