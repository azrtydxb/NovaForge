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
	var pusher uuid.UUID
	if evt.PusherKind == "user" {
		pusher = evt.PusherID
	}
	if _, err := s.ScheduleRun(ctx, evt.OrgID, evt.RepoID, evt.RepoName, evt.Ref, evt.NewSHA, pusher); err != nil {
		// A push to a repository with no workflow is the ordinary case, not
		// a failure worth retrying the message for.
		if errors.Is(err, ErrNoWorkflow) || errors.Is(err, ErrInvalidWorkflow) {
			return nil
		}
		return err
	}
	return nil
}

// ErrNoWorkflow reports that the commit being scheduled declares no
// workflow, or declares one with no jobs. A push finding no workflow is
// ordinary and silent; a person asking for a run on that same commit needs
// to be told why nothing happened, which is why this is a value rather than
// a nil return.
var ErrNoWorkflow = errors.New("the commit declares no CI workflow")

// ErrInvalidWorkflow reports a workflow file that does not parse. Like
// ErrNoWorkflow it is terminal for the commit — redelivering the push would
// re-read the same broken file — so the push consumer acks rather than
// retrying, while a person who asked for the run is told what is wrong.
var ErrInvalidWorkflow = errors.New("the commit's CI workflow is invalid")

// ScheduleRun reads the workflow at commitSHA and creates the Run and its
// Jobs. It is the single path a run is created by — a push event and an
// on-demand request must schedule identically, or CI would behave
// differently depending on how it was asked. ctx must already carry an
// identity able to read the repository. The returned bool is false when the
// run already existed, which is how an at-least-once redelivery of the same
// push stays idempotent.
//
// triggeredBy is the member who pushed or asked for the run, or uuid.Nil when
// no person did; it sponsors the run's agent jobs.
func (s *Scheduler) ScheduleRun(ctx context.Context, orgID, repoID uuid.UUID, repoName, ref, commitSHA string, triggeredBy uuid.UUID) (Run, error) {
	blob, err := s.git.GetBlob(ctx, &gitv1.GetBlobRequest{
		Repo: repoID.String(),
		Ref:  commitSHA,
		Path: workflowPath,
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return Run{}, ErrNoWorkflow
		}
		return Run{}, fmt.Errorf("fetch workflow: %w", err)
	}

	wf, err := ParseWorkflow(blob.Content)
	if err != nil {
		// A workflow file that fails to parse cannot ever succeed on
		// redelivery, so it is reported (for operator visibility) but not
		// retried forever.
		s.log.Error("ci scheduler: invalid workflow", "repo", repoID, "commit", commitSHA, "error", err)
		return Run{}, fmt.Errorf("%w: %s: %v", ErrInvalidWorkflow, workflowPath, err)
	}
	if len(wf.Jobs) == 0 {
		return Run{}, ErrNoWorkflow
	}

	run, created, err := s.store.CreateRun(ctx, Run{
		OrgID:       orgID,
		RepoID:      repoID,
		RepoName:    repoName,
		CommitSHA:   commitSHA,
		Ref:         ref,
		TriggeredBy: triggeredBy,
	})
	if err != nil {
		return Run{}, fmt.Errorf("create run: %w", err)
	}
	if !created {
		// Already scheduled by an earlier, at-least-once delivery of the
		// same event, or by an earlier request for the same commit. The
		// existing run is read back rather than returning the zero value:
		// a caller that asked for a run wants the run, and a redelivered
		// push simply ignores it.
		return s.store.GetRunForCommit(ctx, repoID, commitSHA, ref)
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
			return Run{}, fmt.Errorf("create job %q: %w", name, err)
		}
	}
	return run, nil
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
