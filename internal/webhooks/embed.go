package webhooks

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrationsFS embed.FS

// MigrationsFS is the webhook tables, rooted so its entries are the migration
// files themselves. Apply it with
// database.MigrateAs(url, "gitplatform", "gitplatform_webhooks", MigrationsFS),
// after gitops.MigrationsFS: these tables reference gitplatform.repositories,
// and golang-migrate needs its own tracking table per independently versioned
// migration set applied to a shared schema (see database.MigrateAs).
var MigrationsFS = mustSub(migrationsFS, "migrations")

func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
