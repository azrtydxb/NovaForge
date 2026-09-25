package graph

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/authz"
)

// ErrIndexDeleted is permanent: immutable repository/organization IDs are never reused.
var ErrIndexDeleted = errors.New("index scope was deleted")

type indexSessionKey struct{}
type indexSession struct {
	conn      *pgxpool.Conn
	org, repo uuid.UUID
}

// LockIndex binds all index writes to the session that owns the locks. It must
// be used serially, and release must run after the last write. Per-file commits
// survive a later session failure; no write can outlive the session's fence.
// Org locks always precede repo locks, including in purge and standalone writes.
func (s *Store) LockIndex(ctx context.Context, org, repo uuid.UUID) (context.Context, func(), error) {
	if err := authz.RequireOrg(ctx, org); err != nil {
		return ctx, nil, err
	}
	if org == uuid.Nil || repo == uuid.Nil {
		return ctx, nil, fmt.Errorf("index requires organization and repository")
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return ctx, nil, err
	}
	session := &indexSession{conn: conn, org: org, repo: repo}
	release := func() {
		if session.conn == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// A cancelled query may leave an aborted transaction. Reset before unlock,
		// and destroy rather than pool a session whose lock release is uncertain.
		_, err := conn.Exec(cleanup, "ROLLBACK; SELECT pg_advisory_unlock_all()")
		if err != nil {
			_ = conn.Conn().Close(cleanup)
		}
		conn.Release()
		session.conn = nil
	}
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock_shared(hashtextextended($1,0))`, orgLockKey(org)); err == nil {
		_, err = conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, repoLockKey(org, repo))
	}
	if err == nil {
		err = checkIndexLive(ctx, conn, org, repo)
	}
	if err != nil {
		release()
		return ctx, nil, err
	}
	return context.WithValue(ctx, indexSessionKey{}, session), release, nil
}

func orgLockKey(org uuid.UUID) string        { return "graph:org:" + org.String() }
func repoLockKey(org, repo uuid.UUID) string { return org.String() + ":" + repo.String() }

func checkIndexLive(ctx context.Context, q querier, org, repo uuid.UUID) error {
	var deleted bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM graph.index_tombstones WHERE org_id=$1 AND (repo_id=$2 OR repo_id=$3))`, org, repo, uuid.Nil).Scan(&deleted); err != nil {
		return err
	}
	if deleted {
		return ErrIndexDeleted
	}
	return nil
}

// beginIndexWrite also fences callers outside the push worker. A tombstone
// check without these transaction locks would race deletion before COMMIT.
func beginIndexWrite(ctx context.Context, pool *pgxpool.Pool, org, repo uuid.UUID) (pgx.Tx, error) {
	if org == uuid.Nil {
		return nil, fmt.Errorf("index write requires organization scope")
	}
	var tx pgx.Tx
	var err error
	if session, ok := ctx.Value(indexSessionKey{}).(*indexSession); ok {
		if session.conn == nil {
			return nil, fmt.Errorf("index session released")
		}
		if session.org != org || (repo != uuid.Nil && session.repo != repo) {
			return nil, fmt.Errorf("index session scope mismatch")
		}
		tx, err = session.conn.Begin(ctx)
	} else {
		tx, err = pool.Begin(ctx)
		if err == nil {
			_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, orgLockKey(org))
			if err == nil && repo != uuid.Nil {
				_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, repoLockKey(org, repo))
			}
		}
	}
	if err == nil {
		err = checkIndexLive(ctx, tx, org, repo)
	}
	if err != nil {
		if tx != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = tx.Rollback(cleanup)
			cancel()
		}
		return nil, err
	}
	return tx, nil
}

// SetIndexCheckpoint records (or invalidates, for an empty sha) completion on
// the same session as the file writes. The repo_id also makes purge own legacy
// checkpoint nodes which historically had only the repository UUID as key.
func (s *Store) SetIndexCheckpoint(ctx context.Context, org, repo uuid.UUID, sha, version string) error {
	if err := authz.RequireOrg(ctx, org); err != nil {
		return err
	}
	tx, err := beginIndexWrite(ctx, s.pool, org, repo)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// A partial refresh must not expose the previous compiler generation
	// beside new syntax nodes. Invalidate evidence and its nodes atomically with
	// the checkpoint, using the same fenced connection as publication.
	if sha == "" {
		if _, err := tx.Exec(ctx, `DELETE FROM graph.semantic_snapshots WHERE org_id=$1 AND repo_id=$2`, org, repo); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM graph.graph_nodes WHERE org_id=$1 AND repo_id=$2 AND attrs->>'semantic'='true'`, org, repo); err != nil {
			return err
		}
	}
	attrs := map[string]string{"extraction_version": version}
	if sha != "" {
		attrs["sha"] = sha
	}
	if _, err := upsertNode(ctx, tx, org, repo, true, Node{OrgID: org, Kind: "commit", Key: repo.String(), Attrs: attrs}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// indexReader uses the fenced connection even for reads: retaining one pool
// connection then waiting for another can deadlock a saturated indexer pool.
func (s *Store) indexReader(ctx context.Context, org, repo uuid.UUID) (querier, error) {
	if org == uuid.Nil || repo == uuid.Nil {
		return nil, fmt.Errorf("index read requires organization and repository")
	}
	if err := authz.RequireOrg(ctx, org); err != nil {
		return nil, err
	}
	if session, ok := ctx.Value(indexSessionKey{}).(*indexSession); ok {
		if session.conn == nil {
			return nil, fmt.Errorf("index session released")
		}
		if session.org != org || session.repo != repo {
			return nil, fmt.Errorf("index session scope mismatch")
		}
		return session.conn, nil
	}
	return s.pool, nil
}

// IndexCheckpoint returns only completion evidence for the named extraction contract.
func (s *Store) IndexCheckpoint(ctx context.Context, org, repo uuid.UUID, version string) (string, error) {
	q, err := s.indexReader(ctx, org, repo)
	if err != nil {
		return "", err
	}
	var sha *string
	err = q.QueryRow(ctx, `SELECT attrs->>'sha' FROM graph.graph_nodes WHERE org_id=$1 AND kind='commit' AND key=$2 AND attrs->>'extraction_version'=$3`, org, repo.String(), version).Scan(&sha)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if sha == nil {
		return "", nil
	}
	return *sha, nil
}

// IndexPaths includes partial graph and vector progress, so reconciliation also
// removes source that disappeared after a failed attempt.
func (s *Store) IndexPaths(ctx context.Context, org, repo uuid.UUID) ([]string, error) {
	q, err := s.indexReader(ctx, org, repo)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT attrs->>'path' FROM graph.graph_nodes
 WHERE org_id=$1 AND repo_id=$2 AND kind IN ('file','symbol') AND attrs ? 'path'
 UNION SELECT path FROM graph.code_chunks WHERE org_id=$1 AND repo_id=$2`, org, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}
