package gates_test

import (
	"context"
	"os"
	"testing"
)

func TestSandboxJournalSchema(t *testing.T) {
	pool := storePool(t)
	var exists bool
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass('gates.sandbox_invocations') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("durable sandbox invocation ledger is absent")
	}
	// DOWN must refuse even settled evidence/tombstones, not silently erase it.
	down, err := os.ReadFile("migrations/000005_sandbox_invocations.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), string(down)); err == nil {
		t.Fatal("DOWN silently discarded lifecycle authority")
	}
}
