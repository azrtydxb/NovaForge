package gates_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/cleanup"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/gates"
)

func TestSandboxPurgeRetainsUnresolvedObligations(t *testing.T) {
	for _, organization := range []bool{false, true} {
		name := "runs"
		if organization {
			name = "organization"
		}
		t.Run(name, func(t *testing.T) {
			j, pool, target := journalFor(t, 3)
			for _, schema := range []string{"approvals", "secrets"} {
				sandboxOK(t, database.Migrate(dbURL(t), schema, os.DirFS("../"+schema+"/migrations")))
			}
			org := uuid.New()
			ctx := scopedCtx(org)
			id := reserveSandbox(t, j, ctx)
			foreign := reserveSandbox(t, j, scopedCtx(uuid.New()))
			_, err := j.ClaimCreate(ctx, id)
			sandboxOK(t, err)
			uid := uuid.NewString()
			sandboxOK(t, j.BindPodUID(ctx, id, uid))
			evaluation := uuid.New()
			_, err = pool.Exec(ctx, `INSERT INTO gates.gate_evaluations (id,org_id,run_id,gate,status,target_sha,policy_sha) VALUES ($1,$2,$3,'tests','pass',$4,$5)`, evaluation, org, id.RunID, id.SourceSHA, id.PolicySHA)
			sandboxOK(t, err)
			handler := cleanup.Gates(&gates.Purger{Pool: pool})
			purge := func() error {
				if organization {
					return handler.OrgDeleted(context.Background(), events.OrgDeletedEvent{OrgID: org})
				}
				return handler.RunsDeleted(context.Background(), events.RunsDeletedEvent{OrgID: org, RunIDs: []uuid.UUID{id.RunID}})
			}
			if err = purge(); err == nil || !strings.Contains(err.Error(), "sandbox obligations") {
				t.Fatalf("purge must retain unresolved sandbox obligations, got %v", err)
			}
			var closed bool
			if organization {
				sandboxOK(t, pool.QueryRow(ctx, `SELECT closed FROM secrets.credential_organizations WHERE org_id=$1`, org).Scan(&closed))
			} else {
				sandboxOK(t, pool.QueryRow(ctx, `SELECT closed FROM secrets.credential_scopes WHERE org_id=$1 AND run_id=$2`, org, id.RunID).Scan(&closed))
			}
			if !closed {
				t.Fatal("pending sandbox left credential admission open")
			}
			var exists bool
			sandboxOK(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gates.gate_evaluations WHERE org_id=$1 AND id=$2)`, org, evaluation).Scan(&exists))
			if !exists {
				t.Fatal("purge removed evaluation before sandbox settlement")
			}
			_, err = j.ClaimExec(ctx, id, uid)
			sandboxError(t, err, gates.ErrSandboxFenced)
			// Recovery retains access after deletion fencing; a purge cannot erase the
			// original target or substitute resource absence for terminal evidence.
			terminal, cleanup, absence := sandboxReceipts(uid)
			sandboxOK(t, j.RecordTerminal(ctx, id, terminal))
			sandboxOK(t, j.RecordCleanup(ctx, id, cleanup))
			if err = purge(); err == nil {
				t.Fatal("terminal but not confirmed absent resource allowed purge")
			}
			sandboxOK(t, j.RecordAbsence(ctx, id, absence))
			sandboxOK(t, purge())
			sandboxOK(t, purge())
			sandboxOK(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gates.gate_evaluations WHERE org_id=$1 AND id=$2)`, org, evaluation).Scan(&exists))
			if exists {
				t.Fatal("settled purge retained evaluation")
			}
			v, err := j.ReadExact(ctx, id)
			sandboxOK(t, err)
			if !v.Released || v.Terminal == nil || v.Cleanup == nil || v.Absence == nil {
				t.Fatal("purge erased lifecycle evidence")
			}
			next := sandboxIntent()
			next.RunID = id.RunID
			_, err = j.Reserve(ctx, next)
			sandboxError(t, err, gates.ErrSandboxFenced)
			// A foreign organization's obligation cannot block or be erased by purge.
			_, err = j.ReadExact(scopedCtx(foreign.OrgID), foreign)
			sandboxOK(t, err)
			sandboxCount(t, pool, target, 1)
		})
	}
}
