package gitops

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/events"
)

// WireRedisPushEvents sets PushPublisher to publish one events.PushEvent per
// ref update, over rdb, on events.StreamGitPush, after every successful push
// handled by either the HTTP or the SSH transport. Publish failures are
// logged rather than surfaced to the pushing client, since the push itself
// already succeeded on disk by the time this runs.
//
// gitops has no repository-id concept of its own — repositories are
// identified here only by org id and name — so RepoID is left as uuid.Nil.
// The git-platform service, which does own repository ids, may instead
// assign PushPublisher directly with a closure that resolves the real
// RepoID before calling events.Publish.
func WireRedisPushEvents(rdb *redis.Client) {
	PushPublisher = func(ctx context.Context, scope authz.Scope, orgID uuid.UUID, repo string, updates []RefUpdate) {
		for _, u := range updates {
			evt := events.PushEvent{
				OrgID:    orgID,
				RepoID:   uuid.Nil,
				PusherID: scope.ActorID,
				Ref:      u.Ref,
				OldSHA:   u.OldSHA,
				NewSHA:   u.NewSHA,
				At:       time.Now().UTC(),
			}
			if err := events.Publish(ctx, rdb, events.StreamGitPush, evt); err != nil {
				log.Printf("gitops: publish push event for %s/%s %s: %v", orgID, repo, u.Ref, err)
			}
		}
	}
}
