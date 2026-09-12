package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc/metadata"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/events"
)

// SchedulerGroup is the Redis consumer group the scheduler reads
// events.StreamGitPush under.
const SchedulerGroup = "ci-engine"

// workflowPath is where a workflow definition lives in the repository under
// test.
const workflowPath = ".novaforge/workflow.yaml"

// idleClaimAfter is how long a delivered-but-unacked message must sit idle
// before the autoclaim pass reclaims it for redelivery.
const idleClaimAfter = 60 * time.Second

// autoclaimInterval is how often the autoclaim pass runs.
const autoclaimInterval = 30 * time.Second

// SchedulerConfig configures a Scheduler. Stream and Group default to
// events.StreamGitPush and SchedulerGroup; tests override them to isolate
// each test's traffic on a shared Redis instance.
type SchedulerConfig struct {
	Stream   string
	Group    string
	Consumer string
	// HMACSecret signs the service tokens the scheduler presents when it reads
	// a repository on its own behalf. Without it the scheduler cannot fetch a
	// workflow, so it is required rather than optional.
	HMACSecret string
}

// Scheduler consumes events.StreamGitPush, resolves each push's
// .novaforge/workflow.yaml, and schedules a Run with its Jobs.
type Scheduler struct {
	rdb   *redis.Client
	store *Store
	git   gitv1.GitServiceClient
	cfg   SchedulerConfig
	log   *slog.Logger
}

// NewScheduler builds a Scheduler. git is used to fetch the workflow file at
// the pushed commit.
func NewScheduler(rdb *redis.Client, store *Store, git gitv1.GitServiceClient, cfg SchedulerConfig) *Scheduler {
	if cfg.Stream == "" {
		cfg.Stream = events.StreamGitPush
	}
	if cfg.Group == "" {
		cfg.Group = SchedulerGroup
	}
	if cfg.Consumer == "" {
		cfg.Consumer = "scheduler-" + uuid.NewString()
	}
	return &Scheduler{rdb: rdb, store: store, git: git, cfg: cfg, log: slog.Default()}
}

// Run consumes events.StreamGitPush in the configured consumer group until
// ctx is cancelled, XACKing each message only after its handler returns nil,
// and reclaiming messages idle longer than idleClaimAfter every
// autoclaimInterval.
func (s *Scheduler) Run(ctx context.Context) error {
	if err := events.EnsureGroup(ctx, s.rdb, s.cfg.Stream, s.cfg.Group); err != nil {
		return err
	}

	ticker := time.NewTicker(autoclaimInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.autoclaim(ctx); err != nil {
				s.log.Error("ci scheduler autoclaim", "error", err)
			}
		default:
			if err := s.ProcessAvailable(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				s.log.Error("ci scheduler process", "error", err)
			}
		}
	}
}

// ProcessAvailable reads and handles one batch (up to 10 messages, blocking
// up to 5s for at least one) of pending push events for this consumer,
// XACKing each only after its handler returns nil. It is exported so tests
// can drive the scheduler for a single, deterministic iteration instead of
// running Run's unbounded loop.
func (s *Scheduler) ProcessAvailable(ctx context.Context) error {
	if err := events.EnsureGroup(ctx, s.rdb, s.cfg.Stream, s.cfg.Group); err != nil {
		return err
	}

	res, err := s.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    s.cfg.Group,
		Consumer: s.cfg.Consumer,
		Streams:  []string{s.cfg.Stream, ">"},
		Count:    10,
		Block:    5 * time.Second,
	}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil
		}
		return fmt.Errorf("xreadgroup: %w", err)
	}

	for _, stream := range res {
		for _, msg := range stream.Messages {
			if err := s.handleMessage(ctx, msg); err != nil {
				s.log.Error("ci scheduler handle push event", "id", msg.ID, "error", err)
				continue
			}
			if err := s.rdb.XAck(ctx, s.cfg.Stream, s.cfg.Group, msg.ID).Err(); err != nil {
				s.log.Error("ci scheduler xack", "id", msg.ID, "error", err)
			}
		}
	}
	return nil
}

// autoclaim reclaims messages that have sat unacked for longer than
// idleClaimAfter, handling them as this consumer, so a scheduler that died
// mid-handler never leaves a push event stuck forever.
func (s *Scheduler) autoclaim(ctx context.Context) error {
	start := "0-0"
	for {
		msgs, next, err := s.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream:   s.cfg.Stream,
			Group:    s.cfg.Group,
			Consumer: s.cfg.Consumer,
			MinIdle:  idleClaimAfter,
			Start:    start,
			Count:    50,
		}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				return nil
			}
			return fmt.Errorf("xautoclaim: %w", err)
		}
		for _, msg := range msgs {
			if err := s.handleMessage(ctx, msg); err != nil {
				s.log.Error("ci scheduler handle reclaimed push event", "id", msg.ID, "error", err)
				continue
			}
			if err := s.rdb.XAck(ctx, s.cfg.Stream, s.cfg.Group, msg.ID).Err(); err != nil {
				s.log.Error("ci scheduler xack reclaimed", "id", msg.ID, "error", err)
			}
		}
		if next == "0-0" || len(msgs) == 0 {
			return nil
		}
		start = next
	}
}

func decodePushEvent(raw string) (events.PushEvent, error) {
	var evt events.PushEvent
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		return events.PushEvent{}, err
	}
	return evt, nil
}

func (s *Scheduler) handleMessage(ctx context.Context, msg redis.XMessage) error {
	raw, ok := msg.Values["data"].(string)
	if !ok {
		return fmt.Errorf("push event %s missing data field", msg.ID)
	}
	evt, err := decodePushEvent(raw)
	if err != nil {
		return fmt.Errorf("decode push event %s: %w", msg.ID, err)
	}
	return s.handlePush(ctx, evt)
}

// handlePush schedules a Run for evt, tolerating at-least-once redelivery
// and a repository with no workflow file.
func (s *Scheduler) handlePush(ctx context.Context, evt events.PushEvent) error {
	// The scheduler reacts to an event, not to a request, so it holds no
	// caller's credential. It presents a service token naming the single
	// organization the event belongs to, which is how it reads that
	// repository's workflow without being able to reach any other.
	ctx, err := s.withServiceIdentity(ctx, evt.OrgID)
	if err != nil {
		return err
	}

	blob, err := s.git.GetBlob(ctx, &gitv1.GetBlobRequest{
		Repo: evt.RepoID.String(),
		Ref:  evt.NewSHA,
		Path: workflowPath,
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil
		}
		return fmt.Errorf("fetch workflow: %w", err)
	}

	wf, err := ParseWorkflow(blob.Content)
	if err != nil {
		// A workflow file that fails to parse cannot ever succeed on
		// redelivery, so it is reported (for operator visibility) but not
		// retried forever.
		s.log.Error("ci scheduler: invalid workflow", "repo", evt.RepoID, "commit", evt.NewSHA, "error", err)
		return nil
	}
	if len(wf.Jobs) == 0 {
		return nil
	}

	run, created, err := s.store.CreateRun(ctx, Run{
		OrgID:     evt.OrgID,
		RepoID:    evt.RepoID,
		RepoName:  evt.RepoName,
		CommitSHA: evt.NewSHA,
		Ref:       evt.Ref,
	})
	if err != nil {
		return fmt.Errorf("create run: %w", err)
	}
	if !created {
		// Already scheduled by an earlier, at-least-once delivery of the
		// same event: nothing left to do.
		return nil
	}

	for name, job := range wf.Jobs {
		if _, err := s.store.CreateJob(ctx, WorkflowJob{
			RunID:     run.ID,
			Name:      name,
			Needs:     job.Needs,
			RunCmd:    job.Run,
			AgentRole: job.Agent,
			Image:     job.Image,
			// Only declared paths are kept: collecting everything a job wrote
			// would ship its whole working tree, credentials included.
			ArtifactPaths: job.Artifacts,
		}); err != nil {
			return fmt.Errorf("create job %q: %w", name, err)
		}
	}
	return nil
}

// withServiceIdentity attaches this service's token for orgID to outgoing
// calls.
func (s *Scheduler) withServiceIdentity(ctx context.Context, orgID uuid.UUID) (context.Context, error) {
	tok, err := svcauth.Mint(s.cfg.HMACSecret, "ci-scheduler", orgID, svcauth.DefaultTTL)
	if err != nil {
		return nil, fmt.Errorf("mint service token: %w", err)
	}
	return metadata.AppendToOutgoingContext(ctx,
		"authorization", "Bearer "+tok,
		"x-novaforge-org", orgID.String(),
	), nil
}
