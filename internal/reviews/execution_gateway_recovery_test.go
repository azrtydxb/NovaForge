package reviews

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

func recoveryWorker(s *Store, g *ExecutionGateway) *ReviewWorker {
	return &ReviewWorker{Store: s, Gateway: g, Git: admissionGit{}, Agents: admissionAgents{agent: &agentsv1.Agent{}}, HMACSecret: "review-recovery-fixture", Config: ReviewConfig{WallclockSeconds: 60, MaxInputBytes: 4096, MaxOutputTokens: 128, MaxConcurrentRequests: 1, MaxRoles: 1}}
}

func TestExecutionGatewayFairRecoveryAcrossRestart(t *testing.T) {
	t.Run("within_failed_organization", func(t *testing.T) {
		s := executionTestStore(t)
		f := executionOwner(t, s)
		g := f.gateway(t, s, "original-principal")
		org := uuid.New()
		f.modes("lost", "")
		var ids []uuid.UUID
		for i := 0; i < 9; i++ {
			ctx, r, id := executionRequest(t, s, org)
			if _, _, _, err := invokeExecution(g, ctx, r, id); err == nil {
				t.Fatal("expected lost response")
			}
			if _, err := s.pool.Exec(ctx, `UPDATE reviews.agent_review_requests SET state='failed',lease_until=now()-interval '1 hour' WHERE id=$1`, r.ID); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		f.complete(ids[8])
		for pass := 0; pass < 3; pass++ {
			// Rebuild both the store and SDK client: fairness within an org is
			// persisted, not lost when the consumer restarts with empty memory.
			restarted := NewStore(s.pool)
			worker := recoveryWorker(restarted, f.gateway(t, restarted, "original-principal"))
			_, before := f.counts()
			if err := worker.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			_, after := f.counts()
			if after-before > 4 {
				t.Fatalf("unbounded recovery: %d", after-before)
			}
			if pass < 2 {
				executionHeld(t, s, ids[8], true)
			}
		}
		executionHeld(t, s, ids[8], false)
		for _, id := range ids[:8] {
			executionHeld(t, s, id, true)
		}
		if posts, _ := f.counts(); posts != 9 {
			t.Fatalf("recovery replayed model: %d", posts)
		}
		var approvals int
		if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM reviews.run_reviews`).Scan(&approvals); err != nil || approvals != 0 {
			t.Fatalf("receipt made approval: %d %v", approvals, err)
		}
	})
	t.Run("bounded_round_robin_org_discovery", func(t *testing.T) {
		s := executionTestStore(t)
		f := executionOwner(t, s)
		g := f.gateway(t, s, "original-principal")
		f.modes("lost", "")
		var last uuid.UUID
		for i := 1; i <= 10; i++ {
			org := uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", i))
			ctx, r, id := executionRequest(t, s, org)
			if _, _, _, err := invokeExecution(g, ctx, r, id); err == nil {
				t.Fatal("expected lost response")
			}
			if _, err := s.pool.Exec(ctx, `UPDATE reviews.agent_review_requests SET state='uncertain',lease_until=now()-interval '1 hour' WHERE id=$1`, r.ID); err != nil {
				t.Fatal(err)
			}
			last = id
		}
		f.complete(last)
		worker := recoveryWorker(s, g)
		for i := 0; i < 2; i++ {
			_, before := f.counts()
			if err := worker.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			_, after := f.counts()
			if after-before > 8 {
				t.Fatalf("unbounded organization discovery: %d", after-before)
			}
			if i == 0 {
				executionHeld(t, s, last, true)
			}
		}
		executionHeld(t, s, last, false)
		if _, err := s.reviewOrganizations(executionScope(uuid.New()), uuid.Nil); err == nil {
			t.Fatal("org caller gained platform discovery")
		}
		platform := authz.WithScope(context.Background(), authz.Scope{ActorKind: "service", PlatformWorker: "work-reviews"})
		if _, err := s.takeReviewExecutions(platform); err == nil {
			t.Fatal("platform scope read organization execution data")
		}
	})
}

func TestExecutionGatewayLostResponseLateReceiptDoesNotInventEvidence(t *testing.T) {
	s := executionTestStore(t)
	f := executionOwner(t, s)
	g := f.gateway(t, s, "original-principal")
	org := uuid.New()
	ctx := executionScope(org)
	run, err := s.CreateRun(ctx, Run{OrgID: org, RepoID: uuid.New(), AuthorID: uuid.New(), AuthorKind: "user", Title: "controlled review", SourceRef: "feature", TargetRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err = s.pool.Exec(ctx, `INSERT INTO reviews.agent_review_requests(id,org_id,run_id,requested_by,source_sha,target_sha,state) VALUES($1,$2,$3,$4,$5,$5,'queued')`, id, org, run.ID, uuid.New(), strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	worker := recoveryWorker(s, g)
	worker.Agents = admissionAgents{agent: &agentsv1.Agent{Id: uuid.NewString(), OrgId: org.String(), Role: "reviewer", Enabled: true}}
	f.modes("lost", "")
	if err = worker.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := scanReviewRequest(s.pool.QueryRow(ctx, `SELECT `+requestColumns+` FROM reviews.agent_review_requests WHERE id=$1`, id))
	if err != nil || before.State != "uncertain" || len(before.Attempts) != 1 || before.Attempts[0].TokensAvailable || before.Attempts[0].CostAvailable {
		t.Fatalf("lost response invented evidence: %+v %v", before, err)
	}
	attempt := uuid.MustParse(before.Attempts[0].Id)
	executionHeld(t, s, attempt, true)
	f.complete(attempt)
	restarted := NewStore(s.pool)
	worker = recoveryWorker(restarted, f.gateway(t, restarted, "original-principal"))
	if err = worker.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	executionHeld(t, s, attempt, false)
	after, err := scanReviewRequest(s.pool.QueryRow(ctx, `SELECT `+requestColumns+` FROM reviews.agent_review_requests WHERE id=$1`, id))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatalf("receipt rewrote review evidence: before=%s after=%s", a, b)
	}
	var approvals int
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM reviews.run_reviews WHERE run_id=$1`, run.ID).Scan(&approvals); err != nil || approvals != 0 {
		t.Fatalf("receipt manufactured approval: %d %v", approvals, err)
	}
	if posts, _ := f.counts(); posts != 1 {
		t.Fatalf("recovery dispatched fresh work to resolve uncertainty: %d", posts)
	}
}

func TestExecutionGatewayPrebindingAndPredispatchOrphansRemainHeld(t *testing.T) {
	s := executionTestStore(t)
	f := executionOwner(t, s)
	g := f.gateway(t, s, "original-principal")
	for _, bound := range []bool{false, true} {
		ctx, r, id := executionRequest(t, s, uuid.New())
		if bound {
			if err := s.bindReviewExecution(ctx, r, id, g.owner, strings.Repeat("0", 64)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.pool.Exec(ctx, `UPDATE reviews.agent_review_requests SET lease_until=now()-interval '1 day' WHERE id=$1`, r.ID); err != nil {
			t.Fatal(err)
		}
		if err := g.reconcileReviewExecutions(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := s.claimReview(ctx, recoveryWorker(s, g).Config); err != pgx.ErrNoRows {
			t.Fatalf("orphan no longer blocks admission: %v", err)
		}
		executionHeld(t, s, id, true)
		if _, err := s.pool.Exec(ctx, `UPDATE reviews.review_executions SET owner_url=$2 WHERE attempt_id=$1`, id, g.owner); err == nil && !bound {
			t.Fatal("partially bound identity accepted")
		}
	}
	if posts, _ := f.counts(); posts != 0 {
		t.Fatal("orphan recovery dispatched model")
	}
}

func TestExecutionGatewayMigrationRoundTrip(t *testing.T) {
	s := executionTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL search_path=reviews`); err != nil {
		t.Fatal(err)
	}
	down, err := fs.ReadFile(MigrationsFS, "000006_execution_gateway.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down migration: %v", err)
	}
	up, err := fs.ReadFile(MigrationsFS, "000006_execution_gateway.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, string(up)); err != nil {
		t.Fatalf("up migration after down: %v", err)
	}
}

func TestExecutionGatewayConcurrentDuplicateNeverReplays(t *testing.T) {
	s := executionTestStore(t)
	f := executionOwner(t, s)
	f.entered = make(chan struct{}, 1)
	f.release = make(chan struct{})
	defer func() {
		select {
		case <-f.release:
		default:
			close(f.release)
		}
	}()
	g := f.gateway(t, s, "original-principal")
	ctx, r, id := executionRequest(t, s, uuid.New())
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, _, _, err := invokeExecution(g, ctx, r, id); done <- err }()
	}
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("managed invocation not entered")
	}
	// The loser must fail without waiting for the winner's model completion.
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("duplicate admitted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("duplicate did not hit durable one-shot fence")
	}
	close(f.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("winner did not complete")
	}
	if posts, _ := f.counts(); posts != 1 {
		t.Fatalf("duplicate dispatched %d POSTs", posts)
	}
	executionHeld(t, s, id, false)
}
