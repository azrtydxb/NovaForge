package ci_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/ci"
	"google.golang.org/protobuf/proto"
)

type interruptionAggregateKey struct{}
type holdInterruptionAggregate struct {
	once    sync.Once
	held    chan struct{}
	release chan struct{}
}

func (h *holdInterruptionAggregate) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, interruptionAggregateKey{}, strings.HasPrefix(q.SQL, "UPDATE ci.workflow_runs r SET status=CASE WHEN EXISTS"))
}
func (h *holdInterruptionAggregate) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if hold, _ := ctx.Value(interruptionAggregateKey{}).(bool); hold {
		h.once.Do(func() {
			close(h.held)
			select {
			case <-h.release:
			case <-ctx.Done():
			}
		})
	}
}

// Pause A after its last aggregate snapshot, but before its job failure commits.
// B's ordinary terminal receipt must serialize settlement with that transaction.
func TestTerminalReceiptSerializesWithInterruptedSibling(t *testing.T) {
	s := newCredentialStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connectionA, err := s.store.BeginRunnerConnection(ctx, s.runnerID)
	if err != nil {
		t.Fatal(err)
	}
	jobA := s.job("refs/heads/main", "interrupted-a", "staging")
	if _, _, err = s.store.ClaimForDispatch(ctx, s.runnerID, nil, connectionA); err != nil {
		t.Fatal(err)
	}
	if err = s.store.StartClaimedJob(ctx, jobA, s.runnerID); err != nil {
		t.Fatal(err)
	}
	runID := s.jobState(jobA).RunID
	jobB := s.job("refs/heads/main", "completed-b", "staging")
	if _, err = s.pool.Exec(ctx, `UPDATE ci.workflow_jobs SET run_id=$1 WHERE id=$2`, runID, jobB); err != nil {
		t.Fatal(err)
	}
	rawToken := []byte("owned-rollup-" + uuid.NewString())
	tokenB := hex.EncodeToString(rawToken)
	hash := sha256.Sum256(rawToken)
	runnerB, err := s.store.RegisterRunner(ctx, s.org, "rollup-b", []string{"linux"}, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	connectionB, err := s.store.BeginRunnerConnection(ctx, runnerB)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.store.ClaimForDispatch(ctx, runnerB, nil, connectionB); err != nil {
		t.Fatal(err)
	}
	if err = s.store.StartClaimedJob(ctx, jobB, runnerB); err != nil {
		t.Fatal(err)
	}

	hold := &holdInterruptionAggregate{held: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hold.release) }) }
	defer release()
	cfgA, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfgA.ConnConfig.Tracer = hold
	poolA, err := pgxpool.NewWithConfig(ctx, cfgA)
	if err != nil {
		t.Fatal(err)
	}
	defer poolA.Close()
	// Release before closing the pool even on assertion failure.
	defer release()
	doneA := make(chan error, 1)
	go func() { doneA <- ci.NewStore(poolA).EndRunnerConnection(ctx, s.runnerID, connectionA) }()
	select {
	case <-hold.held:
	case <-ctx.Done():
		t.Fatal("interruption did not reach aggregate barrier")
	}

	cfgB, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	application := "nf_rollup_" + uuid.NewString()
	cfgB.ConnConfig.RuntimeParams["application_name"] = application
	poolB, err := pgxpool.NewWithConfig(ctx, cfgB)
	if err != nil {
		t.Fatal(err)
	}
	defer poolB.Close()
	defer release()
	doneB := make(chan error, 1)
	go func() {
		_, e := ci.NewServer(ci.NewStore(poolB), s.dispatcher).ReportStatus(ctx, &civ1.ReportStatusRequest{
			RunnerId: runnerB.String(), Token: tokenB, ConnectionId: connectionB.String(), JobId: jobB.String(), Status: "success", LastLogSequence: proto.Int64(0),
		})
		doneB <- e
	}()
	// Under the defect B returns without ever waiting; under the correction it
	// waits on A's actual PostgreSQL run lock. No sleep establishes the race.
	bReturned := false
	var resultB error
	for {
		select {
		case resultB = <-doneB:
			bReturned = true
		default:
		}
		if bReturned {
			break
		}
		var waiting bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND application_name=$1 AND wait_event_type='Lock')`, application).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("receipt neither finished nor waited on the run lock")
		case <-time.After(5 * time.Millisecond):
		}
	}
	release()
	if err := <-doneA; err != nil {
		t.Fatal(err)
	}
	if !bReturned {
		resultB = <-doneB
	}
	if resultB != nil {
		t.Fatal(resultB)
	}
	run, err := s.store.GetRun(scopedCtx(s.org), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failure" {
		t.Fatalf("both jobs terminal but owning workflow remains %s", run.Status)
	}
}
