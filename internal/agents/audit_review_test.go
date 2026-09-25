package agents_test

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"os"
	"strings"
	"testing"
	"time"
)

type initialHistoryFailure struct{}

func (initialHistoryFailure) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.Contains(q.SQL, "agents.tool_events") {
		c, cancel := context.WithCancel(ctx)
		cancel()
		return c
	}
	return ctx
}
func (initialHistoryFailure) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type headerStream struct {
	grpc.ServerStream
	ctx       context.Context
	cancel    context.CancelFunc
	headers   int
	headerErr error
}

func (s *headerStream) Context() context.Context                     { return s.ctx }
func (s *headerStream) SendHeader(metadata.MD) error                 { s.headers++; s.cancel(); return s.headerErr }
func (s *headerStream) Send(*agentsv1.StreamRunEventsResponse) error { return nil }
func TestAuditStreamValidIdleHeaderAndInitialErrors(t *testing.T) {
	store := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run := mustCreateRun(t, store, ctx, org)
	rdb := agentsRedis(t)
	for _, tc := range []struct {
		name, cursor string
		failedDB     bool
		want         codes.Code
		headers      int
	}{{"idle", "", false, codes.OK, 1}, {"invalid", run.ID.String() + ":99999", false, codes.InvalidArgument, 0}, {"storage", "", true, codes.Unavailable, 0}, {"header_failure", "", false, codes.Unavailable, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			s := store
			if tc.failedDB {
				cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
				if err != nil {
					t.Fatal(err)
				}
				cfg.ConnConfig.Tracer = initialHistoryFailure{}
				pool, err := pgxpool.NewWithConfig(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer pool.Close()
				s = agents.NewStore(pool)
			}
			c, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			stream := &headerStream{ctx: c, cancel: cancel}
			if tc.name == "header_failure" {
				stream.headerErr = status.Error(codes.Unavailable, "header transport failed")
			}
			err := agents.NewGRPCServer(s, nil, rdb, nil, nil).StreamRunEvents(&agentsv1.StreamRunEventsRequest{RunId: run.ID.String(), AfterCursor: tc.cursor}, stream)
			if status.Code(err) != tc.want || stream.headers != tc.headers {
				t.Fatalf("code=%v headers=%d: %v", status.Code(err), stream.headers, err)
			}
		})
	}
}

func TestAuditPendingCleanupAcknowledgesAndRetriesImmediately(t *testing.T) {
	store := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run := mustCreateRun(t, store, ctx, org)
	a := agents.NewAuditLog(store.Pool())
	if _, err := a.Record(ctx, agents.Entry{RunID: run.ID, Tool: "work.get", ArgsJSON: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteRun(ctx, run.ID, agents.Completion{State: "failed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(ctx, `UPDATE agents.agent_runs SET grant_cleanup_pending=true,work_release_pending=true WHERE id=$1 AND org_id=$2`, run.ID, org); err != nil {
		t.Fatal(err)
	}
	revokes := 0
	store.GrantRevoker = func(context.Context, uuid.UUID) error { revokes++; return nil }
	claims := &workClaimsFixture{}
	store.WorkClaims = claims
	if err := store.CleanupRunGrant(ctx, run.ID); err == nil {
		t.Fatal("pending evidence not reported")
	}
	saved, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.GrantCleanupPending || !saved.WorkReleasePending || saved.WorkReleaseError == "" {
		t.Fatalf("successful revoke not acknowledged: %+v", saved)
	}
	if err = a.ReconcileRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.CleanupRunGrant(ctx, run.ID); err != nil {
		t.Fatalf("immediate retry blocked: %v", err)
	}
	if revokes != 1 {
		t.Fatalf("revoked %d times", revokes)
	}
}
