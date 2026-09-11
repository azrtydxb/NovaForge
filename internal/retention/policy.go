// Package retention holds each organization's data retention policy and the
// sweeper that enforces it. Retention is configurable per organization,
// defaulting to indefinite for provenance and gate outcomes and 90 days for
// raw CI and agent logs.
package retention

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/blobstore"
)

// defaultLogDays and defaultEvidenceDays are the policy an organization
// gets before it ever configures one: 90 days for raw logs, and indefinite
// (0) for provenance and gate outcomes.
const (
	defaultLogDays      = 90
	defaultEvidenceDays = 0
)

// Policy is one organization's retention settings. A 0 value for either
// field means keep indefinitely.
type Policy struct {
	OrgID        uuid.UUID
	LogDays      int
	EvidenceDays int
}

// Store provides access to the retention schema's policy table.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps pool as a retention.Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Get returns orgID's retention policy, or the default policy
// ({LogDays: 90, EvidenceDays: 0}) when the organization has never
// configured one.
func (s *Store) Get(ctx context.Context, orgID uuid.UUID) (Policy, error) {
	p := Policy{OrgID: orgID}
	err := s.pool.QueryRow(ctx, `
		SELECT log_days, evidence_days FROM retention.retention_policies WHERE org_id = $1`,
		orgID,
	).Scan(&p.LogDays, &p.EvidenceDays)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			p.LogDays = defaultLogDays
			p.EvidenceDays = defaultEvidenceDays
			return p, nil
		}
		return Policy{}, fmt.Errorf("get retention policy for org %s: %w", orgID, err)
	}
	return p, nil
}

// Set upserts orgID's retention policy.
func (s *Store) Set(ctx context.Context, policy Policy) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO retention.retention_policies (org_id, log_days, evidence_days)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id) DO UPDATE SET log_days = EXCLUDED.log_days, evidence_days = EXCLUDED.evidence_days`,
		policy.OrgID, policy.LogDays, policy.EvidenceDays,
	)
	if err != nil {
		return fmt.Errorf("set retention policy for org %s: %w", policy.OrgID, err)
	}
	return nil
}

// Sweeper enforces every organization's LogDays setting against the raw CI
// data it governs: sealed job logs and job artifacts, both stored in object
// storage. Provenance and gate-outcome evidence (governed by EvidenceDays)
// live in other services' own schemas — organizations are a hard security
// boundary and no service reads another service's tables, so a shared
// sweeper cannot delete rows it does not own; each service that owns
// EvidenceDays-governed data enforces it itself, consulting the same
// Store.Get this package exposes.
type Sweeper struct {
	pool  *pgxpool.Pool
	blobs *blobstore.Client
}

// NewSweeper builds a Sweeper. pool must be able to see both the retention
// and ci schemas (they share one PostgreSQL cluster per the platform's
// single-cluster, one-schema-per-service model), and blobs is the object
// store sealed logs and artifacts live in.
func NewSweeper(pool *pgxpool.Pool, blobs *blobstore.Client) *Sweeper {
	return &Sweeper{pool: pool, blobs: blobs}
}

// Sweep deletes every sealed job log and job artifact older than its
// organization's LogDays (an organization with no configured policy uses
// the default 90 days), skipping deletion entirely for an organization
// whose LogDays is 0 (keep forever). It returns how many objects (and their
// rows, for artifacts) were deleted.
func (sw *Sweeper) Sweep(ctx context.Context) (int, error) {
	deleted := 0

	n, err := sw.sweepLogs(ctx)
	if err != nil {
		return deleted, err
	}
	deleted += n

	n, err = sw.sweepArtifacts(ctx)
	if err != nil {
		return deleted, err
	}
	deleted += n

	return deleted, nil
}

func (sw *Sweeper) sweepLogs(ctx context.Context) (int, error) {
	rows, err := sw.pool.Query(ctx, `
		SELECT job.id, job.finished_at, COALESCE(pol.log_days, $1)
		FROM ci.workflow_jobs job
		JOIN ci.workflow_runs run ON run.id = job.run_id
		LEFT JOIN retention.retention_policies pol ON pol.org_id = run.org_id
		WHERE job.finished_at IS NOT NULL`,
		defaultLogDays,
	)
	if err != nil {
		return 0, fmt.Errorf("query jobs for log sweep: %w", err)
	}

	type candidate struct {
		jobID      uuid.UUID
		finishedAt time.Time
		logDays    int
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.jobID, &c.finishedAt, &c.logDays); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan job for log sweep: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("query jobs for log sweep: %w", err)
	}
	rows.Close()

	deleted := 0
	now := time.Now()
	for _, c := range candidates {
		if c.logDays == 0 {
			continue
		}
		cutoff := now.AddDate(0, 0, -c.logDays)
		if !c.finishedAt.Before(cutoff) {
			continue
		}

		key := "logs/" + c.jobID.String() + ".txt"
		if err := sw.blobs.Delete(ctx, key); err != nil {
			return deleted, fmt.Errorf("delete sealed log for job %s: %w", c.jobID, err)
		}
		deleted++
	}
	return deleted, nil
}

func (sw *Sweeper) sweepArtifacts(ctx context.Context) (int, error) {
	rows, err := sw.pool.Query(ctx, `
		SELECT art.id, art.object_key, art.created_at, COALESCE(pol.log_days, $1)
		FROM ci.artifacts art
		LEFT JOIN retention.retention_policies pol ON pol.org_id = art.org_id`,
		defaultLogDays,
	)
	if err != nil {
		return 0, fmt.Errorf("query artifacts for sweep: %w", err)
	}

	type candidate struct {
		id        uuid.UUID
		objectKey string
		createdAt time.Time
		logDays   int
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.objectKey, &c.createdAt, &c.logDays); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan artifact for sweep: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("query artifacts for sweep: %w", err)
	}
	rows.Close()

	deleted := 0
	now := time.Now()
	for _, c := range candidates {
		if c.logDays == 0 {
			continue
		}
		cutoff := now.AddDate(0, 0, -c.logDays)
		if !c.createdAt.Before(cutoff) {
			continue
		}

		if err := sw.blobs.Delete(ctx, c.objectKey); err != nil {
			return deleted, fmt.Errorf("delete artifact object %s: %w", c.objectKey, err)
		}
		if _, err := sw.pool.Exec(ctx, `DELETE FROM ci.artifacts WHERE id = $1`, c.id); err != nil {
			return deleted, fmt.Errorf("delete artifact row %s: %w", c.id, err)
		}
		deleted++
	}
	return deleted, nil
}
