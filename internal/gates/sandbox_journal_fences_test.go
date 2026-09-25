package gates_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/gates"
)

func TestSandboxJournalDeletionFences(t *testing.T) {
	j, pool, target := journalFor(t, 5)
	ctx := scopedCtx(uuid.New())
	id := reserveSandbox(t, j, ctx)
	_, err := j.ClaimCreate(ctx, id)
	sandboxOK(t, err)
	sandboxOK(t, j.BindPodUID(ctx, id, "uid"))
	settled, err := j.FenceRuns(ctx, []uuid.UUID{id.RunID})
	sandboxOK(t, err)
	if settled {
		t.Fatal("unresolved obligation allowed purge")
	}
	_, err = j.ClaimExec(ctx, id, "uid")
	sandboxError(t, err, gates.ErrSandboxFenced)
	retry := id.SandboxIntent
	retry.InvocationID, retry.AttemptID = uuid.New(), uuid.New()
	_, err = j.Reserve(ctx, retry)
	sandboxError(t, err, gates.ErrSandboxFenced)
	// Exact readback remains possible; no side-effect authority accompanies it.
	_, err = j.Reserve(ctx, id.SandboxIntent)
	sandboxOK(t, err)
	terminal, cleanup, absence := sandboxReceipts("uid")
	sandboxOK(t, j.RecordTerminal(ctx, id, terminal))
	sandboxOK(t, j.RecordCleanup(ctx, id, cleanup))
	sandboxOK(t, j.RecordAbsence(ctx, id, absence))
	settled, err = j.FenceRuns(ctx, []uuid.UUID{id.RunID})
	sandboxOK(t, err)
	if !settled {
		t.Fatal("settled run remains pending")
	}
	_, err = j.ReadExact(ctx, id)
	sandboxOK(t, err)
	sandboxCount(t, pool, target, 0)
	// The fence applies across configured targets, not just this journal object.
	other, err := gates.NewSandboxJournal(context.Background(), pool, "other-"+uuid.NewString(), "gate-sandbox", 5)
	sandboxOK(t, err)
	_, err = other.Reserve(ctx, retry)
	sandboxError(t, err, gates.ErrSandboxFenced)
	fresh := reserveSandbox(t, other, ctx)
	settled, err = j.FenceOrganization(ctx)
	sandboxOK(t, err)
	if settled {
		t.Fatal("org purge ignored another target's obligation")
	}
	_, err = other.ClaimCreate(ctx, fresh)
	sandboxError(t, err, gates.ErrSandboxFenced)
	_, err = j.Reserve(ctx, sandboxIntent())
	sandboxError(t, err, gates.ErrSandboxFenced)
	_, err = other.Reserve(ctx, sandboxIntent())
	sandboxError(t, err, gates.ErrSandboxFenced)
	// Same global capacity pool, unrelated tenant: org fence is not global.
	_ = reserveSandbox(t, j, scopedCtx(uuid.New()))
}

func TestSandboxJournalFenceAdmissionRace(t *testing.T) {
	for _, orgFence := range []bool{false, true} {
		t.Run(map[bool]string{false: "run", true: "org"}[orgFence], func(t *testing.T) {
			j, _, _ := journalFor(t, 20)
			ctx := scopedCtx(uuid.New())
			run := uuid.New()
			start := make(chan struct{})
			var wg sync.WaitGroup
			var admitted atomic.Int32
			for n := 0; n < 12; n++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					intent := sandboxIntent()
					intent.RunID = run
					_, err := j.Reserve(ctx, intent)
					if err == nil {
						admitted.Add(1)
					} else if !errors.Is(err, gates.ErrSandboxFenced) {
						t.Error(err)
					}
				}()
			}
			close(start)
			var err error
			var settled bool
			if orgFence {
				settled, err = j.FenceOrganization(ctx)
			} else {
				settled, err = j.FenceRuns(ctx, []uuid.UUID{run})
			}
			sandboxOK(t, err)
			wg.Wait()
			if settled != (admitted.Load() == 0) {
				t.Fatalf("fence settled=%v with %d unresolved admissions", settled, admitted.Load())
			}
			for n := 0; n < 3; n++ {
				intent := sandboxIntent()
				intent.RunID = run
				_, err = j.Reserve(ctx, intent)
				sandboxError(t, err, gates.ErrSandboxFenced)
			}
		})
	}
}

func TestSandboxJournalReservationRollback(t *testing.T) {
	j, pool, target := journalFor(t, 1)
	org := uuid.New()
	ctx := scopedCtx(org)
	// Trigger only exists inside the random owned test DB. Force an error AFTER
	// intent INSERT, proving the capacity/intent transaction cannot half-commit.
	_, err := pool.Exec(ctx, `CREATE FUNCTION gates.sandbox_test_reject_capacity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected counter failure'; END $$;
CREATE TRIGGER sandbox_test_reject_capacity BEFORE UPDATE ON gates.sandbox_capacity FOR EACH ROW EXECUTE FUNCTION gates.sandbox_test_reject_capacity()`)
	sandboxOK(t, err)
	intent := sandboxIntent()
	if _, err = j.Reserve(ctx, intent); err == nil {
		t.Fatal("injected update failure ignored")
	}
	sandboxCount(t, pool, target, 0)
	var exists bool
	sandboxOK(t, pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM gates.sandbox_invocations WHERE org_id=$1 AND id=$2)`, org, intent.InvocationID).Scan(&exists))
	if exists {
		t.Fatal("intent survived failed capacity update")
	}
	sandboxOK(t, pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM gates.sandbox_attempts WHERE org_id=$1 AND id=$2)`, org, intent.AttemptID).Scan(&exists))
	if exists {
		t.Fatal("attempt survived failed capacity update")
	}
	// Retry after removing the fault must create exactly one new obligation.
	_, err = pool.Exec(ctx, `DROP TRIGGER sandbox_test_reject_capacity ON gates.sandbox_capacity; DROP FUNCTION gates.sandbox_test_reject_capacity()`)
	sandboxOK(t, err)
	_, err = j.Reserve(ctx, intent)
	sandboxOK(t, err)
	sandboxCount(t, pool, target, 1)
}
