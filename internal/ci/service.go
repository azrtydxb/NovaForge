package ci

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/retention"
)

// sweepInterval is how often the retention sweeper runs.
const sweepInterval = time.Hour

// Service bundles every dependency the ci-runner binary wires together: the
// RunnerService gRPC server (grpc.go), the push-event scheduler, live log
// storage, artifact storage, and the retention sweeper. Constructing it here
// rather than inline in cmd/ci-runner/main.go keeps main.go itself a thin
// composition of Config -> Service -> service.Serve, matching every other
// service binary in this codebase.
type Service struct {
	Store        *Store
	Dispatcher   *Dispatcher
	Logs         *LogSink
	Artifacts    *ArtifactStore
	Server       *Server
	Query        *QueryServer
	Scheduler    *Scheduler
	Sweeper      *retention.Sweeper
	SweepEvery   time.Duration
	RetentionLog *log.Logger
}

// NewService wires a Service from its infrastructure dependencies. git is
// the client the scheduler uses to fetch each push's workflow file.
// hmacSecret signs the service tokens the scheduler presents to git-platform
// when it reads a workflow on its own behalf.
func NewService(pool *pgxpool.Pool, rdb *redis.Client, blobs *blobstore.Client, git gitv1.GitServiceClient, hmacSecret string) *Service {
	store := NewStore(pool)
	dispatcher := NewDispatcher(store)
	logs := NewLogSink(rdb, blobs)
	artifacts := NewArtifactStore(pool, blobs)

	query := NewQueryServer(store, logs, artifacts, blobs)
	server := NewServer(store, dispatcher)
	server.SetLogSink(logs)

	scheduler := NewScheduler(rdb, store, git, SchedulerConfig{HMACSecret: hmacSecret})
	sweeper := retention.NewSweeper(pool, blobs)

	return &Service{
		Store:      store,
		Query:      query,
		Dispatcher: dispatcher,
		Logs:       logs,
		Artifacts:  artifacts,
		Server:     server,
		Scheduler:  scheduler,
		Sweeper:    sweeper,
		SweepEvery: sweepInterval,
	}
}

// Run starts the scheduler's push-event consumer loop and the retention
// sweeper's ticker, blocking until ctx is cancelled. Both run until ctx is
// done; a failure of either is logged rather than fatal, since neither the
// scheduler nor the sweeper being briefly unavailable should take the
// RunnerService gRPC surface down with it (runners must keep being able to
// register, connect, and report status).
func (s *Service) Run(ctx context.Context) {
	go func() {
		if err := s.Scheduler.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("ci-runner: scheduler stopped: %v", err)
		}
	}()

	go s.runSweeper(ctx)
}

func (s *Service) runSweeper(ctx context.Context) {
	interval := s.SweepEvery
	if interval <= 0 {
		interval = sweepInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := s.Sweeper.Sweep(ctx)
			if err != nil {
				log.Printf("ci-runner: retention sweep failed: %v", err)
				continue
			}
			if n > 0 {
				log.Printf("ci-runner: retention sweep deleted %d object(s)", n)
			}
		}
	}
}
