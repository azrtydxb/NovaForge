package knowledge

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrationsFS embed.FS

// MigrationsFS is the knowledge schema's migrations, rooted so its entries are the
// migration files themselves (as database.Migrate's iofs source expects),
// rather than nested under "migrations/".
var MigrationsFS = mustSubFS(migrationsFS, "migrations")

func mustSubFS(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
