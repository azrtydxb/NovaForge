package gitops

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/authz"
)

// beginBlobUpload commits the cleanup intent before starting external I/O. The
// returned transaction locks that intent throughout the upload. Collectors skip
// active uploads; a crash releases the lock and leaves a durable retry record.
func beginBlobUpload(ctx context.Context, pool *pgxpool.Pool, org uuid.UUID, key string) (pgx.Tx, error) {
	if _, err := pool.Exec(ctx, `INSERT INTO gitplatform.blob_cleanup(blob_key,org_id,available_at) VALUES($1,$2,now()+interval '1 hour')`, key, org); err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	var locked string
	if err = tx.QueryRow(ctx, `SELECT blob_key FROM gitplatform.blob_cleanup WHERE blob_key=$1 FOR UPDATE`, key).Scan(&locked); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// BlobCollector only deletes exact durable keys. References in independent forks
// retain a shared immutable payload until the final repository drops it.
type BlobCollector struct {
	Pool  *pgxpool.Pool
	Blobs BlobStore
}

func (c *BlobCollector) CollectOne(ctx context.Context) (bool, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || (!scope.IsPlatformWorker() && scope.OrgID == uuid.Nil) {
		return false, fmt.Errorf("blob cleanup requires organization or platform authority")
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var id, org uuid.UUID
	// Platform inventory reads only opaque identifiers. Every key access below
	// re-enters that owner; an ordinary caller can only drain its own queue.
	err = tx.QueryRow(ctx, `SELECT id,org_id FROM gitplatform.blob_cleanup WHERE available_at<=now() AND ($1 OR org_id=$2) ORDER BY available_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, scope.IsPlatformWorker(), scope.OrgID).Scan(&id, &org)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ctx = authz.WithScope(ctx, authz.Scope{OrgID: org, ActorKind: "service", ServiceName: "git-platform"})
	var key string
	if err = tx.QueryRow(ctx, `SELECT blob_key FROM gitplatform.blob_cleanup WHERE id=$1 AND org_id=$2`, id, org).Scan(&key); err != nil {
		return false, err
	}
	// Reference existence is a worker-only opaque boolean, including references
	// retained after an authorized ownership transfer. No foreign payload is read.
	var referenced bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gitplatform.lfs_objects WHERE blob_key=$1) OR EXISTS(SELECT 1 FROM gitplatform.release_assets WHERE blob_key=$1)`, key).Scan(&referenced); err != nil {
		return false, err
	}
	if !referenced {
		if err = c.Blobs.Delete(ctx, key); err != nil {
			if _, saveErr := tx.Exec(ctx, `UPDATE gitplatform.blob_cleanup SET attempts=attempts+1,last_error=$2,available_at=now()+interval '1 minute' WHERE blob_key=$1 AND org_id=$3`, key, err.Error(), org); saveErr != nil {
				return false, saveErr
			}
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return false, commitErr
			}
			return true, fmt.Errorf("delete queued blob: %w", err)
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM gitplatform.blob_cleanup WHERE blob_key=$1 AND org_id=$2`, key, org); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
func (c *BlobCollector) Run(ctx context.Context) error {
	ctx = authz.WithScope(ctx, authz.Scope{ActorKind: "service", PlatformWorker: "git-blob-cleanup"})
	if c.Blobs == nil {
		return errors.New("blob collector requires object storage")
	}
	for ctx.Err() == nil {
		call, cancel := context.WithTimeout(ctx, 30*time.Second)
		more, err := c.CollectOne(call)
		cancel()
		if err != nil {
			log.Printf("git-platform: blob cleanup: %v", err)
		}
		if more && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return ctx.Err()
}
