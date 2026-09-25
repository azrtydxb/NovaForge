package deployment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/secrets"
	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

type expiryAgentOwner struct {
	agentsv1.AgentServiceClient
	run *agentsv1.Run
}

func (o *expiryAgentOwner) GetRun(context.Context, *agentsv1.GetRunRequest, ...grpc.CallOption) (*agentsv1.GetRunResponse, error) {
	return &agentsv1.GetRunResponse{Run: o.run}, nil
}

type expiryGrantOwner struct {
	identityv1.IdentityServiceClient
	grant *identityv1.Grant
}

func (o *expiryGrantOwner) GetGrant(context.Context, *identityv1.GetGrantRequest, ...grpc.CallOption) (*identityv1.GetGrantResponse, error) {
	return &identityv1.GetGrantResponse{Grant: o.grant}, nil
}
func resolveExpiryOwners(s *Service, ctx context.Context, req Request, grantExpiry, runExpiry time.Time) *expiryGrantOwner {
	scope, _ := authz.FromContext(ctx)
	id := uuid.NewString()
	agent := &expiryAgentOwner{run: &agentsv1.Run{Id: req.RunID.String(), OrgId: scope.OrgID.String(), AgentId: scope.ActorID.String(), RepoId: req.RepoID.String(), GrantId: id, State: "running", StartedAt: runExpiry.Add(-time.Hour).Format(time.RFC3339Nano), WallclockLimitSeconds: 3600}}
	grant := &expiryGrantOwner{grant: &identityv1.Grant{Id: id, OrgId: scope.OrgID.String(), SubjectId: scope.ActorID.String(), SubjectKind: "agent", DeployProd: true, ExpiresAt: grantExpiry.Format(time.RFC3339Nano)}}
	s.resolveRun = NewRunResolver(agent, grant, nil)
	return grant
}

type expiryCredentialOwner struct {
	materializationOwner
	returned time.Time
	revoked  []secrets.DeploymentCredentialRequest
}

func (o *expiryCredentialOwner) PrepareDeployment(ctx context.Context, r secrets.DeploymentCredentialRequest) (secrets.DeploymentCredential, error) {
	c, err := o.materializationOwner.PrepareDeployment(ctx, r)
	if !o.returned.IsZero() {
		c.ExpiresAt = o.returned
	}
	return c, err
}
func (o *expiryCredentialOwner) RevokeDeployment(ctx context.Context, r secrets.DeploymentCredentialRequest) (secrets.CleanupStatus, error) {
	o.revoked = append(o.revoked, r)
	return o.materializationOwner.RevokeDeployment(ctx, r)
}

func TestAuthorizedExpiryShortGrantAndRunBeforeHelm(t *testing.T) {
	for _, kind := range []string{"grant", "run"} {
		t.Run(kind, func(t *testing.T) {
			s, ctx, admin, req, _ := fixture(t)
			short := time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond)
			grant, run := short, time.Now().Add(time.Hour)
			if kind == "run" {
				grant, run = run, short
			}
			resolveExpiryOwners(s, ctx, req, grant, run)
			owner := &expiryCredentialOwner{}
			h, api := recoveryHelm(t, &partialCredentials{})
			client := fake.NewSimpleClientset()
			client.PrependReactor("create", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
				obj, err := createMaterialization(client, a)
				return true, obj, err
			})
			bindRecovery(t, s, req, h)
			m, err := NewSecretMaterializer(s.pool, client, owner, uuid.NewString(), []CredentialBinding{{Target: *s.targets[req.Target], Namespace: h.config.ExecutionNamespace, Name: "RELEASE_KUBECONFIG"}})
			if err != nil {
				t.Fatal(err)
			}
			h.credentials = m
			op, err := s.Request(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			approve(t, s, admin, op)
			got, err := s.Execute(ctx, op.ID)
			if err == nil || got.State != StateFailed || api.posts != 0 {
				t.Errorf("insufficient authority reached Helm: state=%s posts=%d err=%v", got.State, api.posts, err)
			}
			for _, r := range owner.requests {
				if r.ExpiresAt.After(short) {
					t.Errorf("owner request widened %s ceiling: %s > %s", kind, r.ExpiresAt, short)
				}
			}
			if len(client.Actions()) != 0 {
				t.Error("insufficient lifetime materialized a Secret")
			}
		})
	}
}

func TestAuthorizedExpiryReplayAndRetryPreserveOriginalOwnerCeiling(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	ceiling := time.Now().Add(7 * time.Minute).UTC().Truncate(time.Microsecond)
	grant := resolveExpiryOwners(s, ctx, req, ceiling, time.Now().Add(time.Hour))
	owner := &expiryCredentialOwner{materializationOwner: materializationOwner{prepareErr: errors.New("controlled issuer refusal")}}
	h, _ := recoveryHelm(t, &partialCredentials{})
	bindRecovery(t, s, req, h)
	m, err := NewSecretMaterializer(s.pool, fake.NewSimpleClientset(), owner, uuid.NewString(), []CredentialBinding{{Target: *s.targets[req.Target], Namespace: h.config.ExecutionNamespace, Name: "RELEASE_KUBECONFIG"}})
	if err != nil {
		t.Fatal(err)
	}
	h.credentials = m
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	grant.grant.ExpiresAt = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
	if _, err = s.Request(ctx, req); err != nil {
		t.Fatal("request replay", err)
	}
	if _, err = s.Execute(ctx, op.ID); err == nil {
		t.Fatal("expected issuer refusal")
	}
	restarted, err := NewService(s.pool, s.approvals, []Target{*s.targets[req.Target]}, s.resolveRun)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Retry(ctx, op.ID); err == nil {
		t.Fatal("expected issuer refusal")
	}
	if len(owner.requests) != 2 || len(owner.revoked) != 2 {
		t.Fatalf("missing issuer calls: %d/%d", len(owner.requests), len(owner.revoked))
	}
	for i, r := range owner.requests {
		if !r.ExpiresAt.Equal(ceiling) {
			t.Errorf("attempt %d extended original ceiling: %s want %s", i, r.ExpiresAt, ceiling)
		}
		if r != owner.revoked[i] {
			t.Error("cleanup changed immutable issuer request")
		}
	}
}

func TestAuthorizedExpiryShorterOwnerHorizonSurvivesMaterializerRestart(t *testing.T) {
	m, s, ctx, op, client, _ := materializerFixture(t)
	expiry := time.Now().Add(7 * time.Minute).UTC().Truncate(time.Microsecond)
	owner := &expiryCredentialOwner{returned: expiry}
	m.owner = owner
	client.PrependReactor("create", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
		obj, err := createMaterialization(client, a)
		return true, obj, err
	})
	cred, err := m.Prepare(ctx, op, "executor", 10*time.Minute)
	if err != nil {
		t.Fatal("valid shorter owner expiry refused", err)
	}
	if !cred.ExpiresAt.Equal(expiry) {
		t.Fatal("owner expiry discarded")
	}
	restarted, err := NewSecretMaterializer(s.pool, client, owner, m.secret, []CredentialBinding{m.bindings[op.Target]})
	if err != nil {
		t.Fatal(err)
	}
	cred, err = restarted.Prepare(ctx, op, "executor", 10*time.Minute)
	if err != nil || !cred.ExpiresAt.Equal(expiry) {
		t.Fatal("restart widened issued horizon", cred, err)
	}
	if len(owner.requests) != 1 {
		t.Fatal("restart reissued credential")
	}
	if err = restarted.Revoke(ctx, op, "executor"); err != nil {
		t.Fatal(err)
	}
	if len(owner.revoked) != 1 || owner.revoked[0] != owner.requests[0] {
		t.Fatal("cleanup changed request")
	}
}

func TestAuthorizedExpiryRevalidationNarrowsAndCannotRenew(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	original := time.Now().Add(9 * time.Minute).UTC().Truncate(time.Microsecond)
	shortened := original.Add(-2 * time.Minute)
	grant := resolveExpiryOwners(s, ctx, req, original, time.Now().Add(time.Hour))
	owner := &expiryCredentialOwner{materializationOwner: materializationOwner{prepareErr: errors.New("controlled issuer refusal")}}
	h, _ := recoveryHelm(t, &partialCredentials{})
	bindRecovery(t, s, req, h)
	m, err := NewSecretMaterializer(s.pool, fake.NewSimpleClientset(), owner, uuid.NewString(), []CredentialBinding{{Target: *s.targets[req.Target], Namespace: h.config.ExecutionNamespace, Name: "RELEASE_KUBECONFIG"}})
	if err != nil {
		t.Fatal(err)
	}
	h.credentials = m
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	grant.grant.ExpiresAt = shortened.Format(time.RFC3339Nano)
	if _, err = s.Execute(ctx, op.ID); err == nil {
		t.Fatal("expected refusal")
	}
	grant.grant.ExpiresAt = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
	if _, err = s.Retry(ctx, op.ID); err == nil {
		t.Fatal("expected refusal")
	}
	stored, err := s.Get(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AuthorizedUntil == nil || !stored.AuthorizedUntil.Equal(original) {
		t.Fatal("original intent was modified")
	}
	if len(owner.requests) != 2 {
		t.Fatal("missing attempts")
	}
	for i, r := range owner.requests {
		if !r.ExpiresAt.Equal(shortened) {
			t.Errorf("attempt %d renewed narrowed authority: %s", i, r.ExpiresAt)
		}
		a := stored.Attempts[i]
		if a.AuthorizedUntil == nil || !a.AuthorizedUntil.Equal(shortened) || a.CredentialExpiresAt == nil || !a.CredentialExpiresAt.Equal(shortened) {
			t.Fatal("attempt ceiling not durable")
		}
	}
}

func TestAuthorizedExpiryOperatorWindowAndCanceledTransportAreSeparate(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	// Humans have no invented grant or run expiry. A transport deadline is only
	// cancellation, not authority persisted into an operation awaiting approval.
	scope, _ := authz.FromContext(ctx)
	scope.ActorKind = "user"
	scope.Role = "member"
	ctx = authz.WithScope(ctx, scope)
	s.resolveRun = func(context.Context, uuid.UUID) (Run, error) {
		return Run{ID: req.RunID, OrgID: scope.OrgID, RepoID: req.RepoID, ActorID: scope.ActorID, ActorKind: "user"}, nil
	}
	call, cancel := context.WithTimeout(ctx, time.Minute)
	op, err := s.Request(call, req)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if op.AuthorizedUntil != nil {
		t.Fatal("transport deadline became authorization")
	}
	approve(t, s, admin, op)
	op, err = s.Execute(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := op.Attempts[0]
	if a.AuthorizedUntil != nil || a.CredentialExpiresAt == nil || !a.CredentialExpiresAt.Equal(a.StartedAt.Add(10*time.Minute)) {
		t.Fatal("operator horizon not pinned to attempt")
	}
	stored, err := s.Get(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Attempts[0].CredentialExpiresAt.Equal(*a.CredentialExpiresAt) {
		t.Fatal("replay changed horizon")
	}
}

func TestAuthorizedExpiryOwnerLifetimeCheckedBeforeHelm(t *testing.T) {
	for _, remaining := range []time.Duration{time.Minute, 7 * time.Minute, 11 * time.Minute} {
		t.Run(remaining.String(), func(t *testing.T) {
			s, ctx, admin, req, _ := fixture(t)
			resolveExpiryOwners(s, ctx, req, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
			owner := &expiryCredentialOwner{returned: time.Now().Add(remaining)}
			h, api := recoveryHelm(t, &partialCredentials{})
			bindRecovery(t, s, req, h)
			client := fake.NewSimpleClientset()
			client.PrependReactor("create", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
				obj, err := createMaterialization(client, a)
				return true, obj, err
			})
			m, err := NewSecretMaterializer(s.pool, client, owner, uuid.NewString(), []CredentialBinding{{Target: *s.targets[req.Target], Namespace: h.config.ExecutionNamespace, Name: "RELEASE_KUBECONFIG"}})
			if err != nil {
				t.Fatal(err)
			}
			h.credentials = m
			op, err := s.Request(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			approve(t, s, admin, op)
			_, err = s.Execute(ctx, op.ID)
			if err == nil {
				t.Fatal("controlled API never accepts a Job")
			}
			want := 0
			if remaining == 7*time.Minute {
				want = 1
			}
			if api.posts != want {
				t.Fatalf("owner horizon %s: Job creates=%d want %d (%v)", remaining, api.posts, want, err)
			}
			if want == 0 && len(client.Actions()) != 0 {
				t.Fatal("unusable lifetime materialized")
			}
		})
	}
}

func applyExpiryMigration(t *testing.T, s *Service, ctx context.Context, direction string) {
	t.Helper()
	sql, err := migrationsFS.ReadFile("migrations/000006_authorized_expiry." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	// This fixture owns the entire randomly named database; never shared schemas.
	if _, err = s.pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizedExpiryLegacyPendingRequiresNewApproval(t *testing.T) {
	s, ctx, admin, req, exec := fixture(t)
	scope, _ := authz.FromContext(ctx)
	target := s.targets[req.Target]
	op := Operation{Request: req, OrgID: scope.OrgID, ActorID: scope.ActorID, ActorKind: scope.ActorKind, Environment: target.Environment, TargetRevision: target.Revision, Destination: target.Destination}
	if err := s.insert(ctx, op); err != nil {
		t.Fatal(err)
	}
	if _, err := s.approvals.EnsureDeployment(ctx, op.ID, op.RunID, actionFor(op.Environment), binding(op)); err != nil {
		t.Fatal(err)
	}
	applyExpiryMigration(t, s, ctx, "down")
	applyExpiryMigration(t, s, ctx, "up")
	op, err := s.Get(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	if _, err = s.Execute(ctx, op.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("legacy pending executed", err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE deployment.operations SET state='failed' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Retry(ctx, op.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("legacy failed retried", err)
	}
	if exec.calls.Load() != 0 {
		t.Fatal("legacy executor called")
	}
}

func TestAuthorizedExpiryLegacyMaterializationCleanup(t *testing.T) {
	for _, phase := range []string{"creating", "materialized", "absent"} {
		t.Run(phase, func(t *testing.T) {
			m, s, ctx, op, client, _ := materializerFixture(t)
			owner := &expiryCredentialOwner{}
			m.owner = owner
			client.PrependReactor("create", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
				obj, err := createMaterialization(client, a)
				return true, obj, err
			})
			row, err := m.intent(ctx, op, "executor", false)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "creating" {
				if _, err = s.pool.Exec(ctx, `UPDATE deployment.materializations SET phase='creating' WHERE operation_id=$1`, op.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err = m.Prepare(ctx, op, "executor", 10*time.Minute); err != nil {
					t.Fatal(err)
				}
				if phase == "absent" {
					if err = m.Revoke(ctx, op, "executor"); err != nil {
						t.Fatal(err)
					}
				}
			}
			applyExpiryMigration(t, s, ctx, "down")
			applyExpiryMigration(t, s, ctx, "up")
			op, err = s.Get(ctx, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			reloaded, err := m.intent(ctx, op, "executor", false)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.request != row.request {
				t.Fatal("legacy cleanup request changed")
			}
			if phase == "materialized" {
				if reloaded.issuedExpiresAt == nil || !reloaded.issuedExpiresAt.Equal(row.request.ExpiresAt) {
					t.Fatal("confirmed old horizon lost")
				}
			} else if reloaded.issuedExpiresAt != nil {
				t.Fatal("unknown or absent issuance inferred")
			}
			if phase == "absent" && !reloaded.closed {
				t.Fatal("tombstone reopened")
			}
			err = m.Revoke(ctx, op, "executor")
			if phase == "creating" {
				if !errors.Is(err, ErrUncertain) {
					t.Fatal("unknown create falsely clean", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(owner.revoked) == 0 || owner.revoked[len(owner.revoked)-1] != row.request {
				t.Fatal("legacy owner request changed")
			}
		})
	}
}

func TestAuthorizedExpiryCanonicalAcrossProcessTimezones(t *testing.T) {
	instant := time.Now().UTC().Truncate(time.Microsecond)
	local := instant.In(time.FixedZone("restart-zone", 4*60*60))
	op := Operation{AuthorizedUntil: &instant, Attempts: []Attempt{{CredentialExpiresAt: &local}}}
	if got := credentialDeadline(op); got.Location() != time.UTC || !got.Equal(instant) {
		t.Fatal("issuer request expiry is process-timezone dependent", got)
	}
	other := op
	other.AuthorizedUntil = &local
	if binding(op) != binding(other) {
		t.Fatal("approval binding is process-timezone dependent")
	}
}

func TestAuthorizedExpiryRequestReplayNarrowsBeforeLaterExecution(t *testing.T) {
	s, ctx, admin, req, exec := fixture(t)
	original := time.Now().Add(9 * time.Minute).UTC().Truncate(time.Microsecond)
	shortened := original.Add(-2 * time.Minute)
	grant := resolveExpiryOwners(s, ctx, req, original, time.Now().Add(time.Hour))
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	approvedBinding := binding(op)
	grant.grant.ExpiresAt = shortened.Format(time.RFC3339Nano)
	if _, err = s.Request(ctx, req); err != nil {
		t.Fatal(err)
	}
	grant.grant.ExpiresAt = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
	exec.fail = true
	op, err = s.Execute(ctx, op.ID)
	if err == nil {
		t.Fatal("expected fixture failure")
	}
	if binding(op) != approvedBinding {
		t.Fatal("narrowing changed immutable approval")
	}
	if !op.Attempts[0].CredentialExpiresAt.Equal(shortened) {
		t.Fatal("request replay's stricter ceiling was discarded", op.Attempts[0].CredentialExpiresAt)
	}
}

func TestAuthorizedExpiryConcurrentReplayCannotRaceAdmission(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	original := time.Now().Add(9 * time.Minute).UTC().Truncate(time.Microsecond)
	shortened := original.Add(-2 * time.Minute)
	grant := resolveExpiryOwners(s, ctx, req, original, time.Now().Add(time.Hour))
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	// Hold the exact production admission lock from an independent connection,
	// as an executing process would. A narrowing replay must not report success
	// concurrently with that admission or rewrite an already-issued attempt.
	_, unlock, err := s.lock(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	grant.grant.ExpiresAt = shortened.Format(time.RFC3339Nano)
	_, err = s.Request(ctx, req)
	unlock()
	if !errors.Is(err, ErrBusy) {
		t.Fatal("narrowing replay bypassed execution admission lock", err)
	}
	if _, err = s.Request(ctx, req); err != nil {
		t.Fatal(err)
	}
	grant.grant.ExpiresAt = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
	op, err = s.Execute(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !op.Attempts[0].CredentialExpiresAt.Equal(shortened) {
		t.Fatal("admission missed serialized narrowing")
	}
}
