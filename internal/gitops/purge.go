package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/events"
)

// RepoDeletedPublisher announces a repository's deletion to every service
// holding rows keyed on it.
type RepoDeletedPublisher func(ctx context.Context, e events.RepoDeletedEvent) error

// SetRepoDeletedPublisher wires how DeleteRepo announces a deletion. Until it
// is set DeleteRepo refuses to delete anything: a repository removed without
// the announcement leaves every other service's rows for it behind, with
// nothing to say so.
func (s *Server) SetRepoDeletedPublisher(p RepoDeletedPublisher) { s.repoDeleted = p }

// RedisRepoDeletedPublisher publishes on events.StreamRepoDeleted.
func RedisRepoDeletedPublisher(rdb *redis.Client) RepoDeletedPublisher {
	return func(ctx context.Context, e events.RepoDeletedEvent) error {
		return events.Publish(ctx, rdb, events.StreamRepoDeleted, e)
	}
}

// PurgeOrganization removes git-platform's share of the organization in scope:
// its repository records, its capability grants and its bare repositories on
// disk. It is how an organization's deletion reaches this service, and is safe
// to repeat.
func (s *Server) PurgeOrganization(ctx context.Context) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return errors.New("purge requires an organization scope")
	}
	// Disk first: the directory is named by the organization id alone, so a
	// retry after a failed row delete still finds it, and a retry after a
	// failed disk removal still has the rows to remove.
	dir := filepath.Join(s.root, scope.OrgID.String())
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove the organization's repositories from disk: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM gitplatform.repositories WHERE org_id = $1`, scope.OrgID); err != nil {
		return fmt.Errorf("purge repository records: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM gitplatform.capability_grants WHERE org_id = $1`, scope.OrgID); err != nil {
		return fmt.Errorf("purge capability grants: %w", err)
	}
	return nil
}
