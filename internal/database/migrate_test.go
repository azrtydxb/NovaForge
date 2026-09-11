package database_test

import (
	"context"
	"os"
	"testing"

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
