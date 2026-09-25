package gates_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/gates"
)

// Check the effective setting at actual PostgreSQL commit, not just the SQL
// source. This is not a power-loss or lost-COMMIT-response transport test.
func TestSandboxJournalRequiresSynchronousAcknowledgements(t *testing.T) {
	pool := storePool(t)
	ctx := context.Background()
	tables := []string{"sandbox_capacity", "sandbox_org_fences", "sandbox_run_fences", "sandbox_attempts", "sandbox_invocations", "sandbox_evaluation_links"}
	_, err := pool.Exec(ctx, `CREATE FUNCTION gates.test_sandbox_durable_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF current_setting('synchronous_commit') <> 'on' THEN
  RAISE EXCEPTION 'sandbox acknowledgement is not synchronous';
 END IF;
 RETURN NEW;
END $$`)
	sandboxOK(t, err)
	t.Cleanup(func() {
		for _, table := range tables {
			_, err := pool.Exec(ctx, fmt.Sprintf("DROP TRIGGER IF EXISTS test_sandbox_durable_commit ON gates.%s", table))
			if err != nil {
				t.Error(err)
			}
		}
		_, err := pool.Exec(ctx, "DROP FUNCTION gates.test_sandbox_durable_commit()")
		if err != nil {
			t.Error(err)
		}
	})
	for _, table := range tables {
		_, err := pool.Exec(ctx, fmt.Sprintf(`CREATE CONSTRAINT TRIGGER test_sandbox_durable_commit
AFTER INSERT OR UPDATE ON gates.%s DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION gates.test_sandbox_durable_commit()`, table))
		sandboxOK(t, err)
	}
	config, err := pgxpool.ParseConfig(dbURL(t))
	sandboxOK(t, err)
	config.ConnConfig.RuntimeParams["synchronous_commit"] = "off"
	asyncPool, err := pgxpool.NewWithConfig(ctx, config)
	sandboxOK(t, err)
	defer asyncPool.Close()
	journal, err := gates.NewSandboxJournal(ctx, asyncPool, "durability-"+uuid.NewString(), "gate-sandbox", 1)
	sandboxOK(t, err)
	orgCtx := scopedCtx(uuid.New())
	id := reserveSandbox(t, journal, orgCtx)
	_, err = journal.ClaimCreate(orgCtx, id)
	sandboxOK(t, err)
	uid := uuid.NewString()
	sandboxOK(t, journal.BindPodUID(orgCtx, id, uid))
	_, err = journal.ClaimExec(orgCtx, id, uid)
	sandboxOK(t, err)
	terminal, cleanup, absence := sandboxReceipts(uid)
	sandboxOK(t, journal.RecordTerminal(orgCtx, id, terminal))
	sandboxOK(t, journal.RecordCleanup(orgCtx, id, cleanup))
	sandboxOK(t, journal.RecordAbsence(orgCtx, id, absence))
	sandboxOK(t, journal.RecordToolResult(orgCtx, id, gates.SandboxToolResult{PodUID: uid, OutputDigest: "sha256:" + strings.Repeat("f", 64), ObservedAt: terminal.ObservedAt}))
	sandboxOK(t, journal.AcceptToolResult(orgCtx, id, uuid.New()))
	_, err = journal.FenceRuns(orgCtx, []uuid.UUID{id.RunID})
	sandboxOK(t, err)
	_, err = journal.FenceOrganization(orgCtx)
	sandboxOK(t, err)
	var setting string
	sandboxOK(t, asyncPool.QueryRow(ctx, "SHOW synchronous_commit").Scan(&setting))
	if setting != "off" {
		t.Fatal("journal changed connection defaults instead of transaction-local durability")
	}
}
