package database_test

import (
	"context"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/novaforge/novaforge/internal/database"
)

func TestMigrateReleasesDedicatedConnection(t *testing.T) {
	pool, err := database.Connect(context.Background(), dbURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, tc := range []struct {
		name      string
		files     fs.FS
		wantError bool
	}{
		{"success", os.DirFS("testdata/migrations"), false},
		{"failed SQL", fstest.MapFS{"000001_bad.up.sql": {Data: []byte("not valid sql;")}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := "migration_release_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			u, err := url.Parse(dbURL(t))
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			q.Set("application_name", schema)
			u.RawQuery = q.Encode()
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
			})
			err = database.Migrate(u.String(), schema, tc.files)
			if (err != nil) != tc.wantError {
				t.Fatalf("migration error: %v", err)
			}
			// PostgreSQL can observe the TCP close asynchronously. An idle
			// dedicated sql.Conn, unlike an idle pool connection, survives
			// sql.DB.Close indefinitely if its owner never releases it.
			deadline := time.Now().Add(time.Second)
			for {
				var active int
				if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM pg_stat_activity WHERE application_name=$1", schema).Scan(&active); err != nil {
					t.Fatal(err)
				}
				if active == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("migration leaked %d dedicated connection(s)", active)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
