package indexing

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/events"
)

// lockRepository serializes production push handlers across replicas. A process
// crash releases this transaction-scoped lock with its database connection;
// an in-process mutex would leave two graph replicas free to interleave files.
func (idx *Indexer) lockRepository(ctx context.Context, org, repo uuid.UUID) (pgx.Tx, error) {
	tx, err := idx.Graph.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, org.String()+":"+repo.String()); err != nil {
		releaseIndexLock(ctx, tx)
		return nil, err
	}
	return tx, nil
}

func releaseIndexLock(ctx context.Context, tx pgx.Tx) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(cleanup)
}

// currentPush treats an event as a wake-up, not as authority to restore an old
// branch state. Redis may redeliver an older failure after a newer event has
// succeeded. Resolve the current default branch while holding the repository
// lock. A discontinuity requires reconciliation rather than applying a delta
// whose base the index never completed.
func (idx *Indexer) currentPush(ctx context.Context, evt events.PushEvent) (events.PushEvent, bool, bool, error) {
	commits, err := idx.Git.ListCommits(ctx, &gitv1.ListCommitsRequest{Repo: evt.RepoID.String(), Ref: evt.Ref, Limit: 1})
	if err != nil {
		return evt, false, false, fmt.Errorf("resolve index head: %w", err)
	}
	if len(commits.GetCommits()) != 1 || commits.GetCommits()[0].GetSha() == "" {
		return evt, false, false, fmt.Errorf("default branch has no indexable head")
	}
	head := commits.GetCommits()[0].GetSha()
	last, err := idx.lastIndexedSHA(ctx, evt.OrgID, evt.RepoID)
	if err != nil {
		return evt, false, false, err
	}
	if last == head {
		return evt, false, true, nil
	}
	full := last == "" || head != evt.NewSHA || (!isZeroSHA(evt.OldSHA) && last != evt.OldSHA) || (isZeroSHA(evt.OldSHA) && last != "")
	evt.NewSHA = head
	if full {
		evt.OldSHA = emptyTreeSHA
	}
	return evt, full, false, nil
}

// Existing paths must join a full reconciliation: files written by a partial
// attempt may have been deleted at the newer head and are absent from its tree.
// Include chunks too, since a failed de-index can remove nodes but leave chunks.
func (idx *Indexer) existingPaths(ctx context.Context, org, repo uuid.UUID) ([]string, error) {
	rows, err := idx.Graph.Pool().Query(ctx, `
		SELECT attrs->>'path' FROM graph.graph_nodes
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
