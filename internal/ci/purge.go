package ci

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/blobstore"
)

// Purger removes a deleted repository's or organization's CI: runs, jobs and
// artifact rows, the artifact objects and sealed logs in object storage, and
// any live log still in Redis — not just the rows, which is all a SQL delete
// would reach, leaving the objects to be paid for forever.
type Purger struct {
	Pool  *pgxpool.Pool
	Logs  *LogSink
	Blobs *blobstore.Client
}

// PurgeRepository removes every CI run of repoID in the caller's organization.
func (p *Purger) PurgeRepository(ctx context.Context, repoID uuid.UUID) error {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return err
	}
	return p.purge(ctx, orgID, &repoID)
}

// PurgeOrganization removes every CI run, runner and retention policy of the
// caller's organization.
func (p *Purger) PurgeOrganization(ctx context.Context) error {
	orgID, err := purgeOrg(ctx)
	if err != nil {
		return err
	}
	if err := p.purge(ctx, orgID, nil); err != nil {
		return err
	}
	if _, err := p.Pool.Exec(ctx, `DELETE FROM ci.runners WHERE org_id = $1`, orgID); err != nil {
		return fmt.Errorf("purge runners: %w", err)
	}
	if _, err := p.Pool.Exec(ctx, `DELETE FROM retention.retention_policies WHERE org_id = $1`, orgID); err != nil {
		return fmt.Errorf("purge retention policy: %w", err)
	}
	return nil
}

func purgeOrg(ctx context.Context) (uuid.UUID, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return uuid.Nil, errors.New("purge requires an organization scope")
	}
	return scope.OrgID, nil
}

// purge deletes objects before rows. The rows are what name the objects, so
// deleting them first would leave a retry with nothing to find; deleting an
// absent object is not an error, so a repeated purge is harmless.
func (p *Purger) purge(ctx context.Context, orgID uuid.UUID, repoID *uuid.UUID) error {
	rows, err := p.Pool.Query(ctx, `
		SELECT j.id FROM ci.workflow_jobs j
		JOIN ci.workflow_runs r ON r.id = j.run_id
		WHERE r.org_id = $1 AND ($2::uuid IS NULL OR r.repo_id = $2)`, orgID, repoID)
	if err != nil {
		return fmt.Errorf("list jobs to purge: %w", err)
	}
	var jobs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	keys, err := p.artifactKeys(ctx, orgID, jobs)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := p.Blobs.Delete(ctx, key); err != nil {
			return fmt.Errorf("delete artifact object: %w", err)
		}
	}
	for _, job := range jobs {
		if err := p.Logs.Delete(ctx, job); err != nil {
			return err
		}
	}
	if _, err := p.Pool.Exec(ctx, `
		DELETE FROM ci.workflow_runs WHERE org_id = $1 AND ($2::uuid IS NULL OR repo_id = $2)`, orgID, repoID); err != nil {
		return fmt.Errorf("purge runs: %w", err)
	}
	return nil
}

func (p *Purger) artifactKeys(ctx context.Context, orgID uuid.UUID, jobs []uuid.UUID) ([]string, error) {
	if len(jobs) == 0 {
		return nil, nil
	}
	rows, err := p.Pool.Query(ctx,
		`SELECT object_key FROM ci.artifacts WHERE org_id = $1 AND job_id = ANY($2)`, orgID, jobs)
	if err != nil {
		return nil, fmt.Errorf("list artifacts to purge: %w", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}
