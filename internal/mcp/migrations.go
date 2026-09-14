package mcp

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrationsFS embed.FS

// MigrationsFS is the mcp schema's migrations, rooted so its entries are the
// migration files themselves (as database.Migrate's iofs source expects),
// rather than nested under "migrations/".
var MigrationsFS = func() fs.FS {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}()
