# foundation 02: PostgreSQL connection and per-service schema migrations

Status: open
Created: 2026-09-11

## Description

Plan step 2 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/database/connect.go`, `internal/database/migrate.go`, `internal/database/migrate_test.go`

Interfaces: produces `database.Connect(ctx context.Context, url string) (*pgxpool.Pool, error)` and `database.Migrate(url string, schema string, fsys fs.FS) error`. Every later service calls `Migrate` with its own schema name and its own embedded migrations directory.

## Acceptance criteria

- [ ] Write the failing test `internal/database/migrate_test.go`: `func TestMigrateCreatesSchema(t *testing.T)` which skips when `TEST_DATABASE_URL` is unset, calls `database.Migrate(url, "testschema", os.DirFS("testdata/migrations"))`, then asserts `SELECT 1 FROM information_schema.schemata WHERE schema_name='testschema'` returns a row. Run `go test ./internal/database/` — expect FAIL with "undefined: database.Migrate".
- [ ] Add dependencies: `go get github.com/jackc/pgx/v5` and `go get github.com/golang-migrate/migrate/v4`.
- [ ] Implement `Connect` returning a `*pgxpool.Pool` with `MaxConns` 25 and a 30s connect timeout, pinging before returning.
- [ ] Implement `Migrate`: open the URL with `search_path=<schema>`, issue `CREATE SCHEMA IF NOT EXISTS <schema>` after validating `schema` matches `^[a-z][a-z0-9_]{0,62}$` and rejecting anything else with `fmt.Errorf("invalid schema name %q", schema)`, then run golang-migrate over `fsys` with `x-migrations-table=schema_migrations_<schema>`.
- [ ] Create `internal/database/testdata/migrations/000001_init.up.sql` containing `CREATE TABLE IF NOT EXISTS probe (id int primary key);` and a matching `.down.sql` with `DROP TABLE IF EXISTS probe;`.
- [ ] Run `TEST_DATABASE_URL=postgres://novaforge:novaforge@localhost:5432/novaforge?sslmode=disable go test ./internal/database/` — expect PASS.
- [ ] Commit as `feat: add database connection pool and per-schema migrations`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
