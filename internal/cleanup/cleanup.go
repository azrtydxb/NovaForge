// Package cleanup is how a deleted repository or organization leaves every
// service, not only the one that owned it.
//
// git-platform announces a repository's deletion and identity an
// organization's; each service consumes the announcement and deletes its own
// share — rows in its own schemas, its objects, its files. No service deletes
// from another's schema: that is the rule the operator script
// hack/purge-orgs.sh had to break, and it is why the platform had no way to
// delete an organization at all. Rows keyed on runs a service cannot trace to a
// repository (gate evaluations, approvals, credential leases) are reached
// through a second announcement naming the runs deleted.
//
// Each handler is idempotent: an event is acknowledged only once handled, and
// a failure part-way is retried from the start.
package cleanup

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/ci"
	"github.com/novaforge/novaforge/internal/events"
	"github.com/novaforge/novaforge/internal/gates"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/graph"
	"github.com/novaforge/novaforge/internal/knowledge"
	"github.com/novaforge/novaforge/internal/mcp"
	"github.com/novaforge/novaforge/internal/reviews"
	"github.com/novaforge/novaforge/internal/work"
)

// Handler is one service's share of deletion. Any of its functions may be nil
// when the service holds nothing for that kind of deletion.
type Handler struct {
	// Service names the consumer group, so each service gets every event.
	Service     string
	RepoDeleted func(ctx context.Context, e events.RepoDeletedEvent) error
	OrgDeleted  func(ctx context.Context, e events.OrgDeletedEvent) error
	RunsDeleted func(ctx context.Context, e events.RunsDeletedEvent) error

	// Streams default to the platform's; tests use their own so they never
	// consume, and act on, another run's real deletions.
	RepoStream, OrgStream, RunsStream string
}

// RunsPublisher announces runs a service deleted.
type RunsPublisher func(ctx context.Context, e events.RunsDeletedEvent) error

// RedisRunsPublisher publishes on events.StreamRunsDeleted.
func RedisRunsPublisher(rdb *redis.Client) RunsPublisher {
	return func(ctx context.Context, e events.RunsDeletedEvent) error {
		return events.Publish(ctx, rdb, events.StreamRunsDeleted, e)
	}
}

// Redis connects to the event bus a service consumes deletions from. A service
// without it would never remove its share of anything, silently, so an empty
// URL is an error rather than a skipped consumer.
func Redis(url string) (*redis.Client, error) {
	if url == "" {
		return nil, fmt.Errorf("REDIS_URL is required: without the event bus this service never removes a deleted repository's or organization's data")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	return redis.NewClient(opts), nil
}

// Run consumes every stream this handler handles until ctx is cancelled. The
// consumer name is the service's, shared by its replicas: a name per pod would
// strand whatever a replaced pod had been handed and never acknowledged.
func (h Handler) Run(ctx context.Context, rdb *redis.Client, consumer string) {
	if h.RepoDeleted != nil {
		go consume(ctx, rdb, or(h.RepoStream, events.StreamRepoDeleted), h.Service, consumer, func(ctx context.Context, data []byte) error {
			var e events.RepoDeletedEvent
			if err := events.Decode(data, &e); err != nil {
				log.Printf("cleanup: %s: dropping undecodable repository deletion: %v", h.Service, err)
				return nil
			}
			return h.RepoDeleted(ctx, e)
		})
	}
	if h.OrgDeleted != nil {
		go consume(ctx, rdb, or(h.OrgStream, events.StreamOrgDeleted), h.Service, consumer, func(ctx context.Context, data []byte) error {
			var e events.OrgDeletedEvent
			if err := events.Decode(data, &e); err != nil {
				log.Printf("cleanup: %s: dropping undecodable organization deletion: %v", h.Service, err)
				return nil
			}
			return h.OrgDeleted(ctx, e)
		})
	}
	if h.RunsDeleted != nil {
		go consume(ctx, rdb, or(h.RunsStream, events.StreamRunsDeleted), h.Service, consumer, func(ctx context.Context, data []byte) error {
			var e events.RunsDeletedEvent
			if err := events.Decode(data, &e); err != nil {
				log.Printf("cleanup: %s: dropping undecodable run deletion: %v", h.Service, err)
				return nil
			}
			return h.RunsDeleted(ctx, e)
		})
	}
}

func consume(ctx context.Context, rdb *redis.Client, stream, group, consumer string, fn func(context.Context, []byte) error) {
	for ctx.Err() == nil {
		if err := events.Consume(ctx, rdb, stream, group, consumer, fn); err != nil && ctx.Err() == nil {
			log.Printf("cleanup: %s on %s stopped: %v (restarting)", group, stream, err)
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func or(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// orgScope re-enters the organization an event names. A deletion is a
// platform worker's act; the organization comes from the event a trusted
// service published, and every delete below carries it as its predicate.
func orgScope(ctx context.Context, orgID uuid.UUID) (context.Context, error) {
	if orgID == uuid.Nil {
		return nil, fmt.Errorf("deletion event names no organization")
	}
	return authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorKind: "service"}), nil
}

// WorkReviews is work-reviews' share: Work Items and their proposals, comments
// and dependencies; Engineering Runs and their plans, proof and reviews.
func WorkReviews(workStore *work.Store, reviewsStore *reviews.Store, publish RunsPublisher) Handler {
	purge := func(ctx context.Context, orgID uuid.UUID, repoID *uuid.UUID) error {
		ctx, err := orgScope(ctx, orgID)
		if err != nil {
			return err
		}
		var ids []uuid.UUID
		if repoID != nil {
			ids, err = reviewsStore.RunIDsForRepository(ctx, *repoID)
		} else {
			ids, err = reviewsStore.RunIDsForOrganization(ctx)
		}
		if err != nil {
			return err
		}
		// Named before they are deleted: once the rows are gone nothing can
		// say which gate evaluations were theirs.
		if len(ids) > 0 {
			if err := publish(ctx, events.RunsDeletedEvent{OrgID: orgID, Kind: "engineering", RunIDs: ids, At: time.Now().UTC()}); err != nil {
				return fmt.Errorf("announce deleted runs: %w", err)
			}
		}
		if repoID != nil {
			if _, err := reviewsStore.PurgeRepository(ctx, *repoID); err != nil {
				return err
			}
			_, err = workStore.PurgeRepository(ctx, *repoID)
			return err
		}
		if _, err := reviewsStore.PurgeOrganization(ctx); err != nil {
			return err
		}
		_, err = workStore.PurgeOrganization(ctx)
		return err
	}
	return Handler{
		Service: "work-reviews",
		RepoDeleted: func(ctx context.Context, e events.RepoDeletedEvent) error {
			return purge(ctx, e.OrgID, &e.RepoID)
		},
		OrgDeleted: func(ctx context.Context, e events.OrgDeletedEvent) error {
			return purge(ctx, e.OrgID, nil)
		},
	}
}

// CI is ci-runner's share: runs, jobs and artifacts, their objects and logs,
// and for an organization its runners and retention policy.
func CI(p *ci.Purger) Handler {
	return Handler{
		Service: "ci-runner",
		RepoDeleted: func(ctx context.Context, e events.RepoDeletedEvent) error {
			ctx, err := orgScope(ctx, e.OrgID)
			if err != nil {
				return err
			}
			return p.PurgeRepository(ctx, e.RepoID)
		},
		OrgDeleted: func(ctx context.Context, e events.OrgDeletedEvent) error {
			ctx, err := orgScope(ctx, e.OrgID)
			if err != nil {
				return err
			}
			return p.PurgeOrganization(ctx)
		},
	}
}

// EngineeringGraph is engineering-graph's share: graph nodes and edges, indexed
// code chunks, and project knowledge.
func EngineeringGraph(graphStore *graph.Store, knowledgeStore *knowledge.Store) Handler {
	return Handler{
		Service: "engineering-graph",
		RepoDeleted: func(ctx context.Context, e events.RepoDeletedEvent) error {
			ctx, err := orgScope(ctx, e.OrgID)
			if err != nil {
				return err
			}
			if err := graphStore.PurgeRepository(ctx, e.RepoID); err != nil {
				return err
			}
			return knowledgeStore.PurgeRepository(ctx, &e.RepoID)
		},
		OrgDeleted: func(ctx context.Context, e events.OrgDeletedEvent) error {
			ctx, err := orgScope(ctx, e.OrgID)
			if err != nil {
				return err
			}
			if err := graphStore.PurgeOrganization(ctx); err != nil {
				return err
			}
			return knowledgeStore.PurgeRepository(ctx, nil)
		},
	}
}

// AgentRuntime is agent-runtime's share: Agent Runs with their provenance and
// tool calls — cancelling any still going — and for an organization its agents.
func AgentRuntime(store *agents.Store, publish RunsPublisher) Handler {
	purge := func(ctx context.Context, orgID uuid.UUID, repoID *uuid.UUID) error {
		ctx, err := orgScope(ctx, orgID)
		if err != nil {
			return err
		}
		ids, err := store.RunIDsToPurge(ctx, repoID)
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			if err := publish(ctx, events.RunsDeletedEvent{OrgID: orgID, Kind: "agent", RunIDs: ids, At: time.Now().UTC()}); err != nil {
				return fmt.Errorf("announce deleted runs: %w", err)
			}
		}
		_, err = store.PurgeRuns(ctx, repoID)
		return err
	}
	return Handler{
		Service: "agent-runtime",
		RepoDeleted: func(ctx context.Context, e events.RepoDeletedEvent) error {
			return purge(ctx, e.OrgID, &e.RepoID)
		},
		OrgDeleted: func(ctx context.Context, e events.OrgDeletedEvent) error {
			return purge(ctx, e.OrgID, nil)
		},
	}
}

// Gates is the gates service's share: evaluations, approval requests and
// credential leases of deleted runs, and for an organization its secrets.
func Gates(p *gates.Purger) Handler {
	return Handler{
		Service: "gates",
		RunsDeleted: func(ctx context.Context, e events.RunsDeletedEvent) error {
			ctx, err := orgScope(ctx, e.OrgID)
			if err != nil {
				return err
			}
			return p.PurgeRuns(ctx, e.RunIDs)
		},
		OrgDeleted: func(ctx context.Context, e events.OrgDeletedEvent) error {
			ctx, err := orgScope(ctx, e.OrgID)
			if err != nil {
				return err
			}
			return p.PurgeOrganization(ctx)
		},
	}
}

// MCPServer is mcp-server's share: an organization's external MCP servers.
func MCPServer(r *mcp.Registry) Handler {
	return Handler{
		Service: "mcp-server",
		OrgDeleted: func(ctx context.Context, e events.OrgDeletedEvent) error {
			ctx, err := orgScope(ctx, e.OrgID)
			if err != nil {
				return err
			}
			return r.PurgeOrganization(ctx)
		},
	}
}

// GitPlatform is git-platform's share of an organization's deletion: its
// repositories, on disk and as records, and its capability grants. A single
// repository's deletion is git-platform's own act and needs no event.
func GitPlatform(s *gitops.Server) Handler {
	return Handler{
		Service: "git-platform",
		OrgDeleted: func(ctx context.Context, e events.OrgDeletedEvent) error {
			ctx, err := orgScope(ctx, e.OrgID)
			if err != nil {
				return err
			}
			return s.PurgeOrganization(ctx)
		},
	}
}
