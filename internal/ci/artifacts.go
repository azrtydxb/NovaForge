package ci

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/blobstore"
)

const artifactUniqueViolation = "23505"

// Artifact is a row in the ci.artifacts table: one file a job produced.
type Artifact struct {
	ID        uuid.UUID
	JobID     uuid.UUID
	Name      string
	SizeBytes int64
	ObjectKey string
	CreatedAt time.Time
}

// ArtifactStore stores CI job artifacts in object storage and indexes them
// in the ci schema.
type ArtifactStore struct {
	pool  *pgxpool.Pool
	blobs *blobstore.Client
}

// NewArtifactStore builds an ArtifactStore backed by pool and blobs.
func NewArtifactStore(pool *pgxpool.Pool, blobs *blobstore.Client) *ArtifactStore {
	return &ArtifactStore{pool: pool, blobs: blobs}
}

// Upload reads size bytes from r and stores them as an artifact named name
// belonging to jobID, at object key artifacts/<orgID>/<jobID>/<name>. If the
// row insert fails — including a duplicate name for the same job — the
// object just written is deleted again, so object storage never ends up
// holding a blob no row references.
func (a *ArtifactStore) Upload(ctx context.Context, jobID uuid.UUID, name string, r io.Reader, size int64) (Artifact, error) {
	var orgID uuid.UUID
	err := a.pool.QueryRow(ctx, `
		SELECT run.org_id
		FROM ci.workflow_jobs job
		JOIN ci.workflow_runs run ON run.id = job.run_id
		WHERE job.id = $1`,
		jobID,
	).Scan(&orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Artifact{}, fmt.Errorf("job %s not found: %w", jobID, err)
		}
		return Artifact{}, fmt.Errorf("resolve org for job %s: %w", jobID, err)
	}

	objectKey := fmt.Sprintf("artifacts/%s/%s/%s", orgID, jobID, name)
	if err := a.blobs.Put(ctx, objectKey, r, size, "application/octet-stream"); err != nil {
		return Artifact{}, fmt.Errorf("upload artifact %q: %w", name, err)
	}

	id := uuid.New()
	var createdAt time.Time
	err = a.pool.QueryRow(ctx, `
		INSERT INTO ci.artifacts (id, org_id, job_id, name, size_bytes, object_key)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at`,
		id, orgID, jobID, name, size, objectKey,
	).Scan(&createdAt)
	if err != nil {
		_ = a.blobs.Delete(ctx, objectKey)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == artifactUniqueViolation {
			return Artifact{}, fmt.Errorf("artifact %q already exists for job %s", name, jobID)
		}
		return Artifact{}, fmt.Errorf("record artifact %q: %w", name, err)
	}

	return Artifact{
		ID: id, JobID: jobID, Name: name, SizeBytes: size, ObjectKey: objectKey, CreatedAt: createdAt,
	}, nil
}

// List returns every artifact produced by any job of runID, oldest first.
func (a *ArtifactStore) List(ctx context.Context, runID uuid.UUID) ([]Artifact, error) {
	rows, err := a.pool.Query(ctx, `
		SELECT art.id, art.job_id, art.name, art.size_bytes, art.object_key, art.created_at
		FROM ci.artifacts art
		JOIN ci.workflow_jobs job ON job.id = art.job_id
		WHERE job.run_id = $1
		ORDER BY art.created_at`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("list artifacts for run %s: %w", runID, err)
	}
	defer rows.Close()

	var artifacts []Artifact
	for rows.Next() {
		var art Artifact
		if err := rows.Scan(&art.ID, &art.JobID, &art.Name, &art.SizeBytes, &art.ObjectKey, &art.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan artifact: %w", err)
		}
		artifacts = append(artifacts, art)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list artifacts for run %s: %w", runID, err)
	}
	return artifacts, nil
}

// Open returns the content of the artifact identified by id.
func (a *ArtifactStore) Open(ctx context.Context, id uuid.UUID) (io.ReadCloser, error) {
	var objectKey string
	err := a.pool.QueryRow(ctx, `SELECT object_key FROM ci.artifacts WHERE id = $1`, id).Scan(&objectKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("artifact %s not found: %w", id, err)
		}
		return nil, fmt.Errorf("look up artifact %s: %w", id, err)
	}
	r, err := a.blobs.Get(ctx, objectKey)
	if err != nil {
		return nil, fmt.Errorf("open artifact %s: %w", id, err)
	}
	return r, nil
}
