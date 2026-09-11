package capability

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrationsFS embed.FS

// MigrationsFS is the capability_grants migration, rooted so its entries
// are the migration files themselves. capability_grants lives in the
// gitplatform schema (see grant.go's hardcoded table references): the
// identity service applies this migration against that schema when it
// issues and resolves grants, and the git-platform service folds it into
// its own startup migration so a fresh deployment is self-sufficient
// regardless of which service starts first.
var MigrationsFS = mustSub(migrationsFS, "migrations")

func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
