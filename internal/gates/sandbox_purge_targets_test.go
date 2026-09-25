package gates_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gates"
)

func TestSandboxPurgeCoversRetiredTargets(t *testing.T) {
	j, pool, _ := journalFor(t, 2)
	for _, schema := range []string{"approvals", "secrets"} {
		sandboxOK(t, database.Migrate(dbURL(t), schema, os.DirFS("../"+schema+"/migrations")))
	}
	retired, err := gates.NewSandboxJournal(context.Background(), pool, "retired-"+uuid.NewString(), "old-sandbox", 2)
	sandboxOK(t, err)
	ctx := scopedCtx(uuid.New())
	first := reserveSandbox(t, j, ctx)
	intent := sandboxIntent()
	intent.RunID = first.RunID
	second, err := retired.Reserve(ctx, intent)
	sandboxOK(t, err)
	for _, item := range []struct {
		journal *gates.SandboxJournal
		id      gates.SandboxIdentity
	}{{j, first}, {retired, second.Identity}} {
		_, err = item.journal.ClaimCreate(ctx, item.id)
		sandboxOK(t, err)
	}
	purger := &gates.Purger{Pool: pool} // No currently configured target needed.
	purge := func() error { return purger.PurgeRuns(ctx, []uuid.UUID{first.RunID}) }
	sandboxError(t, purge(), gates.ErrSandboxPurgePending)
	for _, item := range []struct {
		journal *gates.SandboxJournal
		id      gates.SandboxIdentity
	}{{j, first}, {retired, second.Identity}} {
		// Fencing must not prevent an observer from reconciling the original UID.
		uid := uuid.NewString()
		sandboxOK(t, item.journal.BindPodUID(ctx, item.id, uid))
		terminal, cleanup, absence := sandboxReceipts(uid)
		sandboxOK(t, item.journal.RecordTerminal(ctx, item.id, terminal))
		sandboxOK(t, item.journal.RecordCleanup(ctx, item.id, cleanup))
		sandboxOK(t, item.journal.RecordAbsence(ctx, item.id, absence))
		if item.journal == j {
			sandboxError(t, purge(), gates.ErrSandboxPurgePending)
		}
	}
	sandboxOK(t, purge())
	// Exact reservation readback is not dispatch authority; a new invocation
	// on the retired target must still be refused after successful purge.
	intent.InvocationID = uuid.New()
	intent.AttemptID = uuid.New()
	_, err = retired.Reserve(ctx, intent)
	sandboxError(t, err, gates.ErrSandboxFenced)
}
