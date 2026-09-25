package database_test

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/novaforge/novaforge/internal/database"
)

func dbURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return u
}

func TestMigrateCreatesSchema(t *testing.T) {
	url := dbURL(t)
	if err := database.Migrate(url, "testschema", os.DirFS("testdata/migrations")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()
	var one int
	err = pool.QueryRow(context.Background(),
		`SELECT 1 FROM information_schema.schemata WHERE schema_name='testschema'`).Scan(&one)
	if err != nil {
		t.Fatalf("schema testschema not found: %v", err)
	}
}

func TestMigrateRejectsBadSchemaName(t *testing.T) {
	url := dbURL(t)
	err := database.Migrate(url, "bad-name; DROP TABLE x", os.DirFS("testdata/migrations"))
	if err == nil {
		t.Fatal("want error for invalid schema name")
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	url := dbURL(t)
	for i := 0; i < 2; i++ {
		if err := database.Migrate(url, "testschema2", os.DirFS("testdata/migrations")); err != nil {
			t.Fatalf("Migrate pass %d: %v", i, err)
		}
	}
}

// TestMigrateRacesToCreateTheSchema is a regression test for a first-run failure
// that looked like a code defect and was not one. On a virgin database, two
// services migrating at the same time both find the schema absent, and
// "CREATE SCHEMA IF NOT EXISTS" is not atomic against a concurrent create: the
// loser gets a unique violation on pg_namespace_nspname_index, not a silent
// no-op. It surfaced as internal/gitops failing only on the first run against a
// freshly created database, which reads exactly like a broken migration.
//
// Migrating the same schema concurrently must therefore succeed in every
// goroutine, because by the time the error is seen the schema does exist.
func TestMigrateRacesToCreateTheSchema(t *testing.T) {
	url := dbURL(t)
	// A schema name of its own, so the race is real rather than resolved by an
	// earlier test in this package having already created it.
	schema := "raceschema_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})

	const racers = 6
	errs := make(chan error, racers)
	start := make(chan struct{})
	for range racers {
		go func() {
			<-start
			errs <- database.Migrate(url, schema, os.DirFS("testdata/migrations"))
		}()
	}
	close(start)
	for range racers {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Migrate of a new schema: %v", err)
		}
	}
}
