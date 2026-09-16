package database

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"regexp"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var schemaNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// Migrate creates schema if absent and applies every migration in fsys to
// it, tracking applied versions in a table named after schema. schema is
// validated rather than escaped: it is interpolated into DDL, so a name
// outside the allowed pattern is refused instead of quoted.
//
// This is what a service uses when it owns schema outright: it is the only
// migration source that will ever apply to it, so the tracking table can be
// named directly after the schema.
func Migrate(dbURL, schema string, fsys fs.FS) error {
	return migrate_(dbURL, schema, schema, fsys)
}

// MigrateAs is Migrate, except the applied-versions tracking table is named
// after trackingName rather than schema.
//
// Some Postgres schemas are written to by more than one service's
// migrations — for example "gitplatform", which holds both
// capability_grants (identity's IssueGrant/GetGrant) and repositories
// (git-platform). golang-migrate requires a single migration source to
// recognize every version ever recorded in its tracking table, even ones it
// has nothing left to apply — so two independently versioned migration
// directories cannot safely share one tracking table: whichever runs second
// fails outright ("no migration found for version N") the first time it
// sees a version number its own source doesn't contain. MigrateAs gives
// each independent migration set its own tracking table
// (schema_migrations_<trackingName>) while still applying its DDL into the
// same shared Postgres schema.
func MigrateAs(dbURL, schema, trackingName string, fsys fs.FS) error {
	return migrate_(dbURL, schema, trackingName, fsys)
}

func migrate_(dbURL, schema, trackingName string, fsys fs.FS) error {
	if !schemaNameRe.MatchString(schema) {
		return fmt.Errorf("invalid schema name %q", schema)
	}
	if !schemaNameRe.MatchString(trackingName) {
		return fmt.Errorf("invalid tracking name %q", trackingName)
	}

	u, err := url.Parse(dbURL)
	if err != nil {
		return fmt.Errorf("parse database url: %w", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	db, err := openStdlib(u.String())
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := db.Exec(fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", schema)); err != nil {
		return fmt.Errorf("create schema %s: %w", schema, err)
	}

	src, err := iofs.New(fsys, ".")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	defer src.Close()
	// WithInstance reserves a dedicated sql.Conn that sql.DB.Close cannot
	// release. Own it explicitly, including driver-initialization failures;
	// otherwise every service startup leaves a migration session behind.
	conn, err := db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("migration connection: %w", err)
	}
	defer conn.Close()
	drv, err := postgres.WithConnection(context.Background(), conn, &postgres.Config{
		SchemaName:      schema,
		MigrationsTable: "schema_migrations_" + trackingName,
	})
	if err != nil {
		return fmt.Errorf("migrate driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "postgres", drv)
	if err != nil {
		return fmt.Errorf("migrate instance: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up (schema %s): %w", schema, err)
	}
	return nil
}
