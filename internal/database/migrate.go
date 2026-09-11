package database

import (
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

// Migrate creates schema if absent and applies every migration in fsys to it.
// schema is validated rather than escaped: it is interpolated into DDL, so a
// name outside the allowed pattern is refused instead of quoted.
func Migrate(dbURL, schema string, fsys fs.FS) error {
	if !schemaNameRe.MatchString(schema) {
		return fmt.Errorf("invalid schema name %q", schema)
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
	drv, err := postgres.WithInstance(db, &postgres.Config{
		SchemaName:      schema,
		MigrationsTable: "schema_migrations_" + schema,
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
