package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/gitops"
)

// newArchiveGuard refuses a push to an archived repository, over either
// transport.
//
// It is a CapFunc so both the smart-HTTP handler and the SSH server get it from
// the one place they already share, rather than each growing its own database
// handle and its own check that could drift. CapFunc is called with the ref
// updates for git-receive-pack and with none for git-upload-pack, so reads are
// unaffected: archiving stops a repository's history moving, it does not hide it.
func newArchiveGuard(pool *pgxpool.Pool) gitops.CapFunc {
	return func(ctx context.Context, _ authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		if len(refs) == 0 {
			return nil
		}
		// The transports address a repository by name; resolve by either so the
		// guard holds if a caller ever presents an id.
		column, value := "name", any(repo)
		if id, err := uuid.Parse(repo); err == nil {
			column, value = "id", any(id)
		}
		var archived bool
		err := pool.QueryRow(ctx,
			`SELECT archived FROM gitplatform.repositories WHERE org_id = $1 AND `+column+` = $2`,
			orgID, value).Scan(&archived)
		if errors.Is(err, pgx.ErrNoRows) {
			// Not this guard's business: the transport's own lookup reports a
			// missing repository, and answering here would turn it into a
			// permission error.
			return nil
		}
		if err != nil {
			return fmt.Errorf("check whether %s is archived: %w", repo, err)
		}
		if archived {
			return fmt.Errorf("%w", gitops.ErrArchived)
		}
		return nil
	}
}
