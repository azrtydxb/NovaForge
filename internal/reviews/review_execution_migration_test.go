package reviews

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/database"
	"io/fs"
	"os"
	"testing"
)

func TestReviewExecutionMigrationRetainsLegacyUnknowns(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// An owned transaction-local schema exercises the exact migration against
	// pre-upgrade rows, without downgrading any shared migration history.
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	schema := "review_upgrade_" + uuid.New().String()[:8]
	if _, err = tx.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA %s; SET LOCAL search_path=%s`, schema, schema)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE agent_review_requests(id uuid,org_id uuid,lease_id uuid,state text,attempts jsonb); INSERT INTO agent_review_requests VALUES(gen_random_uuid(),gen_random_uuid(),NULL,'failed','null'),(gen_random_uuid(),gen_random_uuid(),NULL,'uncertain','[]'),(gen_random_uuid(),gen_random_uuid(),NULL,'succeeded','[]')`); err != nil {
		t.Fatal(err)
	}
	migration, err := fs.ReadFile(MigrationsFS, "000005_review_executions.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	var held int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM review_executions WHERE terminated_at IS NULL`).Scan(&held); err != nil || held != 2 {
		t.Fatalf("legacy unknowns not retained: %d %v", held, err)
	}
}
