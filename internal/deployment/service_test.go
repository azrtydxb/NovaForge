package deployment

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/approvals"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
)

type fixtureExecutor struct {
	calls   atomic.Int32
	fail    bool
	started chan struct{}
	release chan struct{}
}

func (e *fixtureExecutor) Execute(ctx context.Context, op Operation) (Result, error) {
	e.calls.Add(1)
	if e.started != nil {
		close(e.started)
		select {
		case <-e.release:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	if e.fail {
		return Result{}, errors.New("controlled execution failure")
	}
	return Result{ExternalID: op.ID.String(), Summary: "fixture applied " + op.Artifact}, nil
}

func (e *fixtureExecutor) Observe(ctx context.Context, op Operation) (Result, error) {
	return Result{ExternalID: op.ID.String(), Summary: "observed fixture"}, nil
}

func (e *fixtureExecutor) ObserveExisting(ctx context.Context, op Operation) (Result, error) {
	return e.Observe(ctx, op)
}

func fixture(t *testing.T) (*Service, context.Context, context.Context, Request, *fixtureExecutor) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Fatal("TEST_DATABASE_URL is required: deployment evidence must use PostgreSQL")
	}
	adminConn, err := pgx.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	name := "deployment_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = adminConn.Exec(context.Background(), "CREATE DATABASE "+name); err != nil {
		adminConn.Close(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := adminConn.Exec(context.Background(), "DROP DATABASE "+name); err != nil {
			t.Errorf("drop own database: %v", err)
		}
		adminConn.Close(context.Background())
	})
	parsed, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	dbURL = parsed.String()
	if err := database.Migrate(dbURL, "approvals", approvals.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(dbURL, "deployment", MigrationsFS); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	org, repo, actor, run := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorID: actor, ActorKind: "agent"})
	admin := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorID: uuid.New(), ActorKind: "user", Role: "owner"})

	exec := &fixtureExecutor{}
	target := Target{Destination: "fixture-destination", Name: "fixture", OrgID: org, RepoID: repo, Environment: "production", Revision: "fixture-v1", Executor: exec}
	resolver := func(context.Context, uuid.UUID) (Run, error) {
		return Run{ID: run, OrgID: org, RepoID: repo, ActorID: actor, ActorKind: "agent", Grant: capability.Grant{OrgID: org, SubjectID: actor, SubjectKind: "agent", DeployProd: true, ExpiresAt: time.Now().Add(time.Hour)}}, nil
	}
	s, err := NewService(pool, approvals.NewStore(pool), []Target{target}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	return s, ctx, admin, Request{ID: uuid.New(), RunID: run, RepoID: repo, Target: "fixture", Artifact: "sha256:" + strings.Repeat("a", 64)}, exec
}

func approve(t *testing.T, s *Service, ctx context.Context, op Operation) {
	t.Helper()
	scope, _ := authz.FromContext(ctx)
	if _, err := s.approvals.Resolve(ctx, op.ID, scope.ActorID, approvals.StateApproved, "fixture approval"); err != nil {
		t.Fatal(err)
	}
}

func TestApprovedDeploymentExecutesAndReplayIsIdempotent(t *testing.T) {
	s, ctx, admin, req, exec := fixture(t)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(ctx, op.ID); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("pending approval executed: %v", err)
	}
	if exec.calls.Load() != 0 {
		t.Fatal("external action before approval")
	}
	approve(t, s, admin, op)
	got, err := s.Execute(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateSucceeded {
		t.Fatalf("state: %s", got.State)
	}
	if _, err := s.Execute(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if exec.calls.Load() != 1 {
		t.Fatal("replay executed external action twice")
	}
	got, err = s.Get(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Attempts) != 1 || got.Attempts[0].State != StateSucceeded || got.Attempts[0].Result.ExternalID != op.ID.String() {
		t.Fatalf("missing durable result: %+v", got)
	}
}

func TestDeploymentBindingAndScope(t *testing.T) {
	s, ctx, admin, req, exec := fixture(t)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	again, err := s.Request(ctx, req)
	if err != nil || again.ID != op.ID {
		t.Fatalf("request replay: %v", err)
	}
	altered := req
	altered.Artifact = "sha256:" + strings.Repeat("b", 64)
	if _, err = s.Request(ctx, altered); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed artifact reuses approval: %v", err)
	}
	other := authz.WithScope(context.Background(), authz.Scope{OrgID: uuid.New(), ActorID: uuid.New(), ActorKind: "user", Role: "owner"})
	if _, err = s.Execute(other, op.ID); err == nil {
		t.Fatal("cross-org execute allowed")
	}
	if _, err = s.Get(other, op.ID); err == nil {
		t.Fatal("cross-org evidence readable")
	}
	s.targets[req.Target].Revision = "fixture-v2"
	if _, err = s.Execute(ctx, op.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed target reuses approval: %v", err)
	}
	if exec.calls.Load() != 0 {
		t.Fatal("invalid bound request executed")
	}
}

func TestDeploymentFailureIsDurableAndRetryIsExplicit(t *testing.T) {
	s, ctx, admin, req, exec := fixture(t)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	exec.fail = true
	if _, err = s.Execute(ctx, op.ID); err == nil {
		t.Fatal("execution failure hidden")
	}
	got, err := s.Get(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateFailed || len(got.Attempts) != 1 || got.Attempts[0].Error == "" {
		t.Fatalf("failure not durable: %+v", got)
	}
	if _, err = s.Execute(ctx, op.ID); !errors.Is(err, ErrRetryRequired) {
		t.Fatalf("failure auto retried: %v", err)
	}
	exec.fail = false
	if _, err = s.Retry(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateSucceeded || len(got.Attempts) != 2 || exec.calls.Load() != 2 {
		t.Fatalf("retry missing evidence: %+v", got)
	}
}

func TestDeploymentRechecksGrantAndRejectsConcurrentExecution(t *testing.T) {
	s, ctx, admin, req, exec := fixture(t)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	original := s.resolveRun
	s.resolveRun = func(ctx context.Context, id uuid.UUID) (Run, error) {
		r, e := original(ctx, id)
		r.Grant.ExpiresAt = time.Now().Add(-time.Minute)
		return r, e
	}
	if _, err = s.Execute(ctx, op.ID); err == nil {
		t.Fatal("expired grant used")
	}
	s.resolveRun = original
	exec.started = make(chan struct{})
	exec.release = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, e := s.Execute(ctx, op.ID); done <- e }()
	<-exec.started
	if _, err = s.Execute(ctx, op.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent execution: %v", err)
	}
	close(exec.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentApprovalRequiresIndependentAdministrator(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	scope, _ := authz.FromContext(ctx)
	adminScope, _ := authz.FromContext(admin)
	tests := []struct {
		name    string
		scope   authz.Scope
		decider uuid.UUID
	}{
		{"agent impersonation", scope, adminScope.ActorID},
		{"self approval", authz.Scope{OrgID: scope.OrgID, ActorID: scope.ActorID, ActorKind: "user", Role: "owner"}, scope.ActorID},
		{"ordinary member", authz.Scope{OrgID: scope.OrgID, ActorID: adminScope.ActorID, ActorKind: "user", Role: "member"}, adminScope.ActorID},
		{"forged decider", adminScope, uuid.New()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req.ID = uuid.New()
			op, err = s.Request(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.approvals.Resolve(authz.WithScope(context.Background(), tc.scope), op.ID, tc.decider, approvals.StateApproved, ""); err == nil {
				t.Fatal("unauthorized deployment approval accepted")
			}
		})
	}
}

func TestAdministrativeRecoveryDoesNotRenewExpiredExecutionAuthority(t *testing.T) {
	s, ctx, admin, req, exec := fixture(t)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	if _, err = crashStart(ctx, s, op); err != nil {
		t.Fatal(err)
	}
	original := s.resolveRun
	s.resolveRun = func(ctx context.Context, id uuid.UUID) (Run, error) {
		r, e := original(ctx, id)
		r.Grant.ExpiresAt = time.Now().Add(-time.Minute)
		return r, e
	}
	if _, err = s.Reconcile(ctx, op.ID); err == nil {
		t.Fatal("expired agent granted recovery authority")
	}
	adminScope, _ := authz.FromContext(admin)
	member := adminScope
	member.Role = "member"
	foreign := adminScope
	foreign.OrgID = uuid.New()
	for _, scope := range []authz.Scope{member, foreign, {OrgID: adminScope.OrgID, ActorID: adminScope.ActorID, ActorKind: "agent", Role: "owner"}} {
		if _, err = s.Reconcile(authz.WithScope(context.Background(), scope), op.ID); err == nil {
			t.Fatal("unauthorized recovery")
		}
	}
	got, err := s.Reconcile(admin, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := got.Attempts[len(got.Attempts)-1]
	if got.State != StateSucceeded || last.Kind != "recover" || last.ActorID != adminScope.ActorID {
		t.Fatalf("recovery attribution missing: %+v", got)
	}
	if exec.calls.Load() != 0 {
		t.Fatal("recovery invoked execution")
	}
	if _, err = s.Execute(admin, op.ID); err == nil {
		t.Fatal("administrator silently acquired author execution authority")
	}
}

func TestDeploymentGrantAndRunScopesFailClosed(t *testing.T) {
	s, ctx, _, req, _ := fixture(t)
	original := s.resolveRun
	cases := []struct {
		name   string
		mutate func(*Run)
	}{
		{"wrong organization", func(r *Run) { r.OrgID = uuid.New() }},
		{"wrong repository", func(r *Run) { r.RepoID = uuid.New() }},
		{"wrong run", func(r *Run) { r.ID = uuid.New() }},
		{"wrong actor", func(r *Run) { r.ActorID = uuid.New() }},
		{"grant subject", func(r *Run) { r.Grant.SubjectID = uuid.New() }},
		{"grant org", func(r *Run) { r.Grant.OrgID = uuid.New() }},
		{"no production grant", func(r *Run) { r.Grant.DeployProd = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s.resolveRun = func(ctx context.Context, id uuid.UUID) (Run, error) {
				r, e := original(ctx, id)
				tc.mutate(&r)
				return r, e
			}
			if _, err := s.Request(ctx, req); err == nil {
				t.Fatal("invalid authorization accepted")
			}
		})
	}
	s.resolveRun = original
	staging := *s.targets[req.Target]
	staging.Name = "staging"
	staging.Environment = "staging"
	s.targets[staging.Name] = &staging
	req.Target = "staging"
	if _, err := s.Request(ctx, req); err == nil {
		t.Fatal("production grant escalated to staging")
	}
	s.resolveRun = func(ctx context.Context, id uuid.UUID) (Run, error) {
		r, e := original(ctx, id)
		r.Grant.DeployStaging = true
		return r, e
	}
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(ctx, op.ID); !errors.Is(err, ErrApprovalRequired) {
		t.Fatal("staging bypassed human approval")
	}
}

func TestHumanDeploymentMembershipDoesNotRequireAgentGrant(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	scope, _ := authz.FromContext(ctx)
	scope.ActorKind = "user"
	scope.Role = "member"
	ctx = authz.WithScope(context.Background(), scope)
	original := s.resolveRun
	s.resolveRun = func(ctx context.Context, id uuid.UUID) (Run, error) {
		r, e := original(ctx, id)
		r.ActorKind = "user"
		r.Grant = capability.Grant{}
		return r, e
	}
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	if _, err = s.Execute(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDistinctDeploymentTargetsDoNotExhaustEvidencePool(t *testing.T) {
	s, ctx, admin, req, first := fixture(t)
	config := s.pool.Config()
	config.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s.pool = pool
	s.approvals = approvals.NewStore(pool)
	second := &fixtureExecutor{started: make(chan struct{}), release: make(chan struct{})}
	first.started = make(chan struct{})
	first.release = make(chan struct{})
	secondTarget := *s.targets[req.Target]
	secondTarget.Name = "other-target"
	secondTarget.Destination = "other-destination"
	secondTarget.Executor = second
	s.targets[secondTarget.Name] = &secondTarget
	firstOp, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, firstOp)
	req.ID = uuid.New()
	req.Target = secondTarget.Name
	secondOp, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, secondOp)
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	done := make(chan error, 2)
	go func() { _, err := s.Execute(callCtx, firstOp.ID); done <- err }()
	<-first.started
	go func() { _, err := s.Execute(callCtx, secondOp.ID); done <- err }()
	select {
	case <-second.started:
	case <-time.After(time.Second):
		t.Error("target locks exhausted the pool needed for durable evidence")
	}
	close(first.release)
	close(second.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Errorf("execute: %v", err)
		}
	}
}

// crashStart commits an attempt on its lock-owning session then closes it,
// simulating a process exit without finalization.
func crashStart(ctx context.Context, s *Service, op Operation) (int, error) {
	conn, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig.Copy())
	if err != nil {
		return 0, err
	}
	defer conn.Close(context.Background())
	var acquired bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, "deployment/"+op.Destination).Scan(&acquired); err != nil {
		return 0, err
	}
	if !acquired {
		return 0, ErrBusy
	}
	return s.start(ctx, conn, op, "execute")
}
