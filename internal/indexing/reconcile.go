package indexing

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/events"
)

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
	return idx.Graph.IndexPaths(ctx, org, repo)
}
