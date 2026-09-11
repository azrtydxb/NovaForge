package gitops

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrationsFS embed.FS

// MigrationsFS is the gitops schema migrations (the repositories table),
// rooted so its entries are the migration files themselves. Apply it with
// database.MigrateAs(url, "gitplatform", "gitplatform_git", MigrationsFS):
// see the comment atop migrations/000001_git.up.sql for why this migration
// needs its own tracking table rather than database.Migrate's default one.
var MigrationsFS = mustSub(migrationsFS, "migrations")

func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
