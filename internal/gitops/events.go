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
// RepoIDFunc resolves a repository's id from its organization and name.
type RepoIDFunc func(ctx context.Context, orgID uuid.UUID, repo string) (uuid.UUID, error)

// WireRedisPushEventsWithRepoID is WireRedisPushEvents with the repository id
// resolved for each event.
//
// A push event whose RepoID is nil is useless to every consumer: the CI
// scheduler looks the repository up by that id to read its workflow, and a nil
// id silently matched nothing, so no run was ever scheduled.
func WireRedisPushEventsWithRepoID(rdb *redis.Client, resolve RepoIDFunc) {
	inner := repoIDPublisher(rdb, resolve)
	PushPublisher = inner
}

func repoIDPublisher(rdb *redis.Client, resolve RepoIDFunc) func(context.Context, authz.Scope, uuid.UUID, string, []RefUpdate) {
	return func(ctx context.Context, scope authz.Scope, orgID uuid.UUID, repo string, updates []RefUpdate) {
		repoID, err := resolve(ctx, orgID, repo)
		if err != nil {
			log.Printf("gitops: resolve repository id for %s/%s: %v", orgID, repo, err)
			return
		}
		for _, u := range updates {
			evt := events.PushEvent{
				OrgID:    orgID,
				RepoID:   repoID,
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
