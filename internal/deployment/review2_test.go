package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// HTTP fixture keeps separate Jobs and logs so an old release cannot accidentally
// stand in for a newer delivery's executor. No Kubernetes or issuer access occurs.
type recoveryAPI struct {
	mu     sync.Mutex
	jobs   map[string]*batchv1.Job
	proofs map[string]HelmEvidence
	posts  int
}

func (a *recoveryAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		a.posts++
		w.WriteHeader(500)
		return
	}
	if strings.Contains(r.URL.Path, "/jobs/") {
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if j := a.jobs[name]; j != nil {
			_ = json.NewEncoder(w).Encode(j)
			return
		}
	} else if strings.HasSuffix(r.URL.Path, "/pods") {
		name := strings.TrimPrefix(r.URL.Query().Get("labelSelector"), "batch.kubernetes.io/job-name=")
		if j := a.jobs[name]; j != nil {
			p := evidencePod(j)
			p.Name = name
			_ = json.NewEncoder(w).Encode(corev1.PodList{Items: []corev1.Pod{p}})
			return
		}
	} else if strings.HasSuffix(r.URL.Path, "/log") {
		parts := strings.Split(r.URL.Path, "/")
		_ = json.NewEncoder(w).Encode(a.proofs[parts[len(parts)-2]])
		return
	}
	w.WriteHeader(404)
	_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","reason":"NotFound","code":404}`))
}
func recoveryHelm(t *testing.T, c CredentialProvisioner) (*HelmExecutor, *recoveryAPI) {
	t.Helper()
	a := &recoveryAPI{jobs: map[string]*batchv1.Job{}, proofs: map[string]HelmEvidence{}}
	server := httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(server.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHelmExecutor(client, helmConfig(), c)
	if err != nil {
		t.Fatal(err)
	}
	return h, a
}
func (a *recoveryAPI) put(h *HelmExecutor, op Operation, terminal bool, state string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	last := op.Attempts[len(op.Attempts)-1]
	name := strings.TrimSuffix(CredentialSecretName(op), "-creds")
	j := h.job(op, last.Kind, name, CredentialSecretName(op))
	j.UID = types.UID(uuid.NewString())
	if terminal {
		j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	}
	a.jobs[name] = j
	a.proofs[name] = HelmEvidence{State: state, Release: h.config.Release, Namespace: h.config.TargetNamespace, Revision: last.Number, Binding: helmBinding(op)}
}
func bindRecovery(t *testing.T, s *Service, req Request, h *HelmExecutor) {
	t.Helper()
	s.targets[req.Target].Executor = h
	s.targets[req.Target].Revision = h.Revision()
	s.targets[req.Target].Destination = h.Destination()
}

func TestReviewRetryCannotUseOldFailedDelivery(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	h, a := recoveryHelm(t, &fixtureCredentials{})
	bindRecovery(t, s, req, h)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	first := op
	first.Attempts = []Attempt{{Number: 1, Kind: "execute"}}
	a.put(h, first, true, StateFailed)
	op, err = s.Execute(ctx, op.ID)
	if !errors.Is(err, ErrDeliveryFailed) {
		t.Fatal(err)
	}
	retry := op
	retry.Attempts = append(retry.Attempts, Attempt{Number: 2, Kind: "execute"})
	a.put(h, retry, false, StateSucceeded)
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	op, err = s.Retry(call, op.ID)
	if !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	observe := op
	observe.Attempts = append(observe.Attempts, Attempt{Number: 3, Kind: "observe"})
	a.put(h, observe, true, StateFailed)
	a.mu.Lock()
	a.proofs[strings.TrimSuffix(CredentialSecretName(observe), "-creds")] = a.proofs[strings.TrimSuffix(CredentialSecretName(first), "-creds")]
	a.mu.Unlock()
	got, err := s.Reconcile(ctx, op.ID)
	if got.State != StateUncertain || !errors.Is(err, ErrUncertain) {
		t.Fatalf("old failed release released active retry: state=%s err=%v", got.State, err)
	}
	if _, err = s.Retry(ctx, op.ID); !errors.Is(err, ErrUncertain) {
		t.Fatalf("retry dispatched: %v", err)
	}
	req.ID = uuid.New()
	next, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, next)
	if _, err = s.Execute(ctx, next.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("successor dispatched: %v", err)
	}
	a.put(h, retry, true, StateSucceeded)
	got, err = s.Reconcile(admin, op.ID)
	// Later durable cleanup tests require pending credentials to keep this uncertain.
	if got.Attempts[len(got.Attempts)-1].Result.ExternalID != "fixture/2" {
		t.Fatalf("completed retry evidence not recovered: %+v %v", got, err)
	}
	if got.State != StateUncertain || !errors.Is(err, ErrUncertain) {
		t.Fatal("passive retry recovery erased pending cleanup")
	}
	if err = s.CleanupCredentials(janitorContext(ctx), op.ID, 2, h.Revision()); err != nil {
		t.Fatal(err)
	}
	got, err = s.Reconcile(admin, op.ID)
	if err != nil || got.State != StateSucceeded || got.Attempts[len(got.Attempts)-1].Result.ExternalID != "fixture/2" {
		t.Fatalf("completed retry not reconciled after cleanup: %s %v", got.State, err)
	}
}

func TestReviewPassiveFindsLatestDeliveryThroughFailedObservations(t *testing.T) {
	h, a := recoveryHelm(t, &fixtureCredentials{})
	op := Operation{Request: Request{ID: uuid.New(), Artifact: "sha256:" + strings.Repeat("a", 64)}, TargetRevision: h.Revision(), Attempts: []Attempt{{Number: 1, Kind: "execute"}}}
	a.put(h, op, true, StateSucceeded)
	op.Attempts = append(op.Attempts, Attempt{Number: 2, Kind: "observe", State: StateUncertain}, Attempt{Number: 3, Kind: "recover", State: StateUncertain}, Attempt{Number: 4, Kind: "observe", State: StateUncertain}, Attempt{Number: 5, Kind: "recover"})
	if result, err := h.ObserveExisting(context.Background(), op); err != nil || result.ExternalID != "fixture/1" {
		t.Fatalf("failed observations stranded delivery: %+v %v", result, err)
	}
	op.Attempts = append(op.Attempts, Attempt{Number: 6, Kind: "execute"}, Attempt{Number: 7, Kind: "recover"})
	if _, err := h.ObserveExisting(context.Background(), op); !errors.Is(err, ErrUncertain) {
		t.Fatalf("fell back across newer execute: %v", err)
	}
	if a.posts != 0 {
		t.Fatal("passive recovery mutated workloads")
	}
}

type partialCredentials struct {
	prepareErr, revokeErr       error
	cancel                      context.CancelFunc
	issued, revoked             []int
	cleanupLive, cleanupBounded bool
}

func (c *partialCredentials) Prepare(ctx context.Context, op Operation, _ string, _ time.Duration) (Credential, error) {
	c.issued = append(c.issued, op.Attempts[len(op.Attempts)-1].Number)
	if c.cancel != nil {
		c.cancel()
	}
	return Credential{}, c.prepareErr
}
func (c *partialCredentials) Revoke(ctx context.Context, op Operation, _ string) error {
	c.revoked = append(c.revoked, op.Attempts[len(op.Attempts)-1].Number)
	c.cleanupLive = ctx.Err() == nil
	d, ok := ctx.Deadline()
	c.cleanupBounded = ok && time.Until(d) > 0 && time.Until(d) <= 11*time.Second
	return c.revokeErr
}
func TestReviewPartialPreparationCompensates(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		uncertain, cancel, revokeFail bool
	}{{"ordinary", false, false, false}, {"uncertain", true, false, false}, {"cancelled", true, true, false}, {"revoke failure", true, false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			s, ctx, admin, req, _ := fixture(t)
			c := &partialCredentials{prepareErr: errors.New("provider secret details")}
			if tc.uncertain {
				c.prepareErr = errors.Join(ErrUncertain, c.prepareErr)
			}
			if tc.revokeFail {
				c.revokeErr = errors.New("revocation secret details")
			}
			h, a := recoveryHelm(t, c)
			bindRecovery(t, s, req, h)
			op, err := s.Request(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			approve(t, s, admin, op)
			call, cancel := context.WithCancel(ctx)
			defer cancel()
			if tc.cancel {
				c.cancel = cancel
			}
			got, err := s.Execute(call, op.ID)
			if len(c.revoked) != 1 || c.revoked[0] != 1 || !c.cleanupLive || !c.cleanupBounded {
				t.Errorf("partial issuance uncompensated: %+v", c)
			}
			if tc.uncertain && (!errors.Is(err, ErrUncertain) || got.State != StateUncertain) {
				t.Errorf("provider uncertainty lost: %s %v", got.State, err)
			}
			if strings.Contains(err.Error(), "secret details") {
				t.Error("provider details leaked")
			}
			if a.posts != 0 {
				t.Fatal("Job created after failed preparation")
			}
		})
	}
}

func janitorContext(ctx context.Context) context.Context {
	scope, _ := authz.FromContext(ctx)
	scope.ActorKind = "service"
	scope.ActorID = uuid.Nil
	scope.Role = ""
	return authz.WithScope(context.Background(), scope)
}
func restartService(t *testing.T, s *Service, req Request) *Service {
	t.Helper()
	restarted, err := NewService(s.pool, s.approvals, []Target{*s.targets[req.Target]}, s.resolveRun)
	if err != nil {
		t.Fatal(err)
	}
	return restarted
}
func assertPending(t *testing.T, s *Service, ctx context.Context, id uuid.UUID, attempt int) {
	t.Helper()
	op, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range op.Credentials {
		if c.Attempt == attempt && c.ResolvedAt == nil {
			return
		}
	}
	t.Fatalf("durable cleanup obligation %d missing: %+v", attempt, op.Credentials)
}
func crashCall(t *testing.T, call func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Error("fixture did not interrupt controller")
		}
	}()
	call()
}

type journalCredentials struct {
	beforePrepare           func(context.Context, Operation)
	beforeRevoke            func(context.Context, Operation)
	prepareErr, errorRevoke error
	issued, revoked         []int
}

func (c *journalCredentials) Prepare(ctx context.Context, op Operation, _ string, _ time.Duration) (Credential, error) {
	c.issued = append(c.issued, op.Attempts[len(op.Attempts)-1].Number)
	if c.beforePrepare != nil {
		c.beforePrepare(ctx, op)
	}
	return Credential{SecretName: CredentialSecretName(op), ExpiresAt: time.Now().Add(10 * time.Minute)}, c.prepareErr
}
func (c *journalCredentials) Revoke(ctx context.Context, op Operation, _ string) error {
	c.revoked = append(c.revoked, op.Attempts[len(op.Attempts)-1].Number)
	if c.beforeRevoke != nil {
		c.beforeRevoke(ctx, op)
	}
	return c.errorRevoke
}

func TestReviewCredentialJournalSurvivesControllerLoss(t *testing.T) {
	for _, point := range []string{"after issuance", "after compensation", "lost lock during issuance", "lost lock after compensation"} {
		t.Run(point, func(t *testing.T) {
			s, ctx, admin, req, _ := fixture(t)
			c := &journalCredentials{}
			h, a := recoveryHelm(t, c)
			bindRecovery(t, s, req, h)
			op, err := s.Request(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			approve(t, s, admin, op)
			c.beforePrepare = func(call context.Context, issued Operation) {
				// A separate real DB connection must see the obligation before issuer I/O.
				assertPending(t, s, ctx, issued.ID, 1)
				var binding, phase string
				if err := s.pool.QueryRow(ctx, `SELECT provider_binding,phase FROM deployment.credential_obligations WHERE org_id=$1 AND operation_id=$2 AND attempt=1`, op.OrgID, op.ID).Scan(&binding, &phase); err != nil || binding != h.Revision() || phase != "preparing" {
					t.Fatalf("issuer preceded durable intent: %s %s %v", binding, phase, err)
				}
				if point == "after issuance" {
					panic("simulated controller loss after issuance")
				}
				if point == "lost lock during issuance" {
					var killed bool
					if err := s.pool.QueryRow(ctx, `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&killed); err != nil || !killed {
						t.Fatalf("owned lock not terminated: %v", err)
					}
				}
			}
			if point == "lost lock after compensation" {
				c.prepareErr = ErrUncertain
				c.beforeRevoke = func(context.Context, Operation) {
					var killed bool
					if err := s.pool.QueryRow(ctx, `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&killed); err != nil || !killed {
						t.Fatalf("owned revoke lock not terminated: %v", err)
					}
				}
			}
			if point == "after compensation" {
				c.prepareErr = ErrUncertain
				c.beforeRevoke = func(context.Context, Operation) {
					panic("simulated controller loss after provider compensation before acknowledgement")
				}
			}
			if point == "lost lock during issuance" || point == "lost lock after compensation" {
				if _, err = s.Execute(ctx, op.ID); !errors.Is(err, ErrUncertain) {
					t.Fatalf("lost session dispatched: %v", err)
				}
			} else {
				crashCall(t, func() { _, _ = s.Execute(ctx, op.ID) })
			}
			s = restartService(t, s, req)
			assertPending(t, s, ctx, op.ID, 1)
			if _, err = s.Retry(ctx, op.ID); !errors.Is(err, ErrUncertain) {
				t.Fatalf("restart allowed retry: %v", err)
			}
			if _, err = s.pool.Exec(ctx, `DELETE FROM deployment.credential_obligations WHERE org_id=$1 AND operation_id=$2`, op.OrgID, op.ID); err == nil {
				t.Fatal("purge discarded pending cleanup")
			}
			c.beforePrepare = nil
			c.beforeRevoke = nil
			if err = s.CleanupCredentials(janitorContext(ctx), op.ID, 1, h.Revision()); err != nil {
				t.Fatal(err)
			}
			if len(c.issued) != 1 || a.posts != 0 {
				t.Fatal("cleanup issued credentials or created workload")
			}
			got, err := s.Reconcile(admin, op.ID)
			if got.State != StateFailed || !errors.Is(err, ErrDeliveryFailed) {
				t.Fatalf("confirmed no-dispatch could not recover: %s %v", got.State, err)
			}
		})
	}
}

func TestReviewCredentialJournalRefusesUnregisteredIssuance(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	c := &journalCredentials{}
	h, _ := recoveryHelm(t, c)
	bindRecovery(t, s, req, h)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	_, err = s.pool.Exec(ctx, `CREATE FUNCTION deployment.refuse_credential() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'controlled journal outage'; END $$; CREATE TRIGGER refuse_credential BEFORE INSERT ON deployment.credential_obligations FOR EACH ROW EXECUTE FUNCTION deployment.refuse_credential()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(ctx, op.ID); err == nil {
		t.Fatal("journal failure allowed execute")
	}
	if len(c.issued) != 0 || len(c.revoked) != 0 {
		t.Fatal("provider accessed without durable intent")
	}
}

func TestReviewCredentialCleanupConfirmationIsExactAndFenced(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	c := &partialCredentials{prepareErr: ErrUncertain, revokeErr: errors.New("provider unavailable")}
	h, _ := recoveryHelm(t, c)
	bindRecovery(t, s, req, h)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	if _, err = s.Execute(ctx, op.ID); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	assertPending(t, s, ctx, op.ID, 1)
	service := janitorContext(ctx)
	scope, _ := authz.FromContext(service)
	scope.OrgID = uuid.New()
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		attempt int
		binding string
	}{
		{"human", admin, 1, h.Revision()}, {"author", ctx, 1, h.Revision()}, {"foreign org", authz.WithScope(context.Background(), scope), 1, h.Revision()},
		{"wrong attempt", service, 2, h.Revision()}, {"wrong binding", service, 1, "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(c.revoked)
			if err := s.CleanupCredentials(tc.ctx, op.ID, tc.attempt, tc.binding); err == nil {
				t.Fatal("invalid cleanup confirmation accepted")
			}
			if len(c.revoked) != before {
				t.Fatal("invalid scope reached issuer")
			}
			assertPending(t, s, ctx, op.ID, 1)
		})
	}
	if err = s.CleanupCredentials(service, op.ID, 1, h.Revision()); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	assertPending(t, s, ctx, op.ID, 1)
	c.revokeErr = nil
	if err = s.CleanupCredentials(service, op.ID, 1, h.Revision()); err != nil {
		t.Fatal(err)
	}
	before := len(c.revoked)
	if err = s.CleanupCredentials(service, op.ID, 1, h.Revision()); err != nil || len(c.revoked) != before {
		t.Fatal("exact cleanup replay not idempotent")
	}
}

func TestReviewPassiveRecoveryRetainsCleanupThroughFailedObservations(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	c := &partialCredentials{prepareErr: ErrUncertain}
	h, a := recoveryHelm(t, c)
	bindRecovery(t, s, req, h)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	execution := op
	execution.Attempts = []Attempt{{Number: 1, Kind: "execute"}}
	a.put(h, execution, false, StateSucceeded)
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err = s.Execute(call, op.ID); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = s.Reconcile(ctx, op.ID); !errors.Is(err, ErrUncertain) {
			t.Fatal(err)
		}
		if _, err = s.Reconcile(admin, op.ID); !errors.Is(err, ErrUncertain) {
			t.Fatal(err)
		}
	}
	if len(c.issued) != 2 {
		t.Fatalf("failed observations not exercised: %+v", c)
	}
	beforePrepare, beforeRevoke := len(c.issued), len(c.revoked)
	// Janitor must not revoke the live delivery even when its controller is gone.
	if err = s.CleanupCredentials(janitorContext(ctx), op.ID, 1, h.Revision()); !errors.Is(err, ErrUncertain) {
		t.Fatal("live delivery credential revoked")
	}
	if len(c.revoked) != beforeRevoke {
		t.Fatal("active Job reached revoke")
	}
	a.put(h, execution, true, StateSucceeded)
	resolve := s.resolveRun
	s.resolveRun = func(c context.Context, id uuid.UUID) (Run, error) {
		r, e := resolve(c, id)
		r.Grant.ExpiresAt = time.Now().Add(-time.Hour)
		return r, e
	}
	if _, err = s.Reconcile(ctx, op.ID); err == nil {
		t.Fatal("expired author reconciled")
	}
	got, err := s.Reconcile(admin, op.ID)
	last := got.Attempts[len(got.Attempts)-1]
	if got.State != StateUncertain || !errors.Is(err, ErrUncertain) || last.Result.ExternalID != "fixture/1" {
		t.Fatalf("passive evidence lost or cleanup bypassed: %+v %v", got, err)
	}
	if len(c.issued) != beforePrepare || len(c.revoked) != beforeRevoke || a.posts != 0 {
		t.Fatal("passive recovery mutated credentials/workloads")
	}
	assertPending(t, s, ctx, op.ID, 1)
	if err = s.CleanupCredentials(janitorContext(ctx), op.ID, 1, h.Revision()); err != nil {
		t.Fatal(err)
	}
	beforeRevoke = len(c.revoked)
	got, err = s.Reconcile(admin, op.ID)
	if err != nil || got.State != StateSucceeded || got.Attempts[len(got.Attempts)-1].Result.ExternalID != "fixture/1" {
		t.Fatalf("persisted cleanup confirmation not honored: %s %v", got.State, err)
	}
	if len(c.issued) != beforePrepare || len(c.revoked) != beforeRevoke || a.posts != 0 {
		t.Fatal("passive completion mutated credentials/workloads")
	}
}

func TestReviewAttemptBindingRejectsOlderTerminalEvidence(t *testing.T) {
	h, a := recoveryHelm(t, &fixtureCredentials{})
	op := Operation{Request: Request{ID: uuid.New(), Artifact: "sha256:" + strings.Repeat("a", 64)}, TargetRevision: h.Revision(), Attempts: []Attempt{{Number: 1, Kind: "execute"}}}
	oldBinding := helmBinding(op)
	op.Attempts = append(op.Attempts, Attempt{Number: 2, Kind: "execute"})
	a.put(h, op, true, StateFailed)
	name := strings.TrimSuffix(CredentialSecretName(op), "-creds")
	a.mu.Lock()
	proof := a.proofs[name]
	proof.Binding = oldBinding
	a.proofs[name] = proof
	a.mu.Unlock()
	op.Attempts = append(op.Attempts, Attempt{Number: 3, Kind: "recover"})
	if _, err := h.ObserveExisting(context.Background(), op); !errors.Is(err, ErrUncertain) {
		t.Fatalf("older attempt evidence accepted: %v", err)
	}
}

func TestReviewObservationRequiresStoppedDeliveryExecutor(t *testing.T) {
	h, a := recoveryHelm(t, &fixtureCredentials{})
	op := Operation{Request: Request{ID: uuid.New(), Artifact: "sha256:" + strings.Repeat("a", 64)}, TargetRevision: h.Revision(), Attempts: []Attempt{{Number: 1, Kind: "execute"}}}
	a.put(h, op, false, StateFailed)
	op.Attempts = append(op.Attempts, Attempt{Number: 2, Kind: "observe"})
	a.put(h, op, true, StateFailed)
	name := strings.TrimSuffix(CredentialSecretName(op), "-creds")
	a.mu.Lock()
	job := a.jobs[name].DeepCopy()
	a.mu.Unlock()
	if _, err := h.evidence(context.Background(), job, op); !errors.Is(err, ErrUncertain) {
		t.Fatalf("terminal observation released active delivery: %v", err)
	}
	delivery := op
	delivery.Attempts = op.Attempts[:1]
	a.put(h, delivery, true, StateUncertain)
	// Status reconciliation is useful even when the stopped delivery lost logs.
	// Its terminal executor, not its log's verdict, is the required fence.
	if _, err := h.evidence(context.Background(), job, op); !errors.Is(err, ErrDeliveryFailed) {
		t.Fatalf("terminal delivery with uncertain log blocked current status evidence: %v", err)
	}
}

func TestReviewAuthorRetriesPreparationCleanupWithoutIssuance(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	c := &partialCredentials{prepareErr: errors.New("partial issuance"), revokeErr: errors.New("unavailable")}
	h, a := recoveryHelm(t, c)
	bindRecovery(t, s, req, h)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	if _, err = s.Execute(ctx, op.ID); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	assertPending(t, s, ctx, op.ID, 1)
	c.revokeErr = nil
	got, err := s.Reconcile(ctx, op.ID)
	if got.State != StateFailed || !errors.Is(err, ErrDeliveryFailed) {
		t.Fatalf("cleanup-only author recovery: %s %v", got.State, err)
	}
	if len(c.issued) != 1 || a.posts != 0 {
		t.Fatal("cleanup-only recovery issued new credentials/workload")
	}
	for _, c := range got.Credentials {
		if c.ResolvedAt == nil {
			t.Fatalf("cleanup left unresolved: %+v", c)
		}
	}
}

func TestReviewAmbiguousCreateDoesNotCompensatePossiblyActiveJob(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	c := &journalCredentials{}
	h, a := recoveryHelm(t, c)
	bindRecovery(t, s, req, h)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	got, err := s.Execute(ctx, op.ID)
	if got.State != StateUncertain || !errors.Is(err, ErrUncertain) || a.posts == 0 {
		t.Fatalf("ambiguous create fixture failed: %s %v posts=%d", got.State, err, a.posts)
	}
	if len(got.Credentials) != 1 || got.Credentials[0].Phase != "dispatching" {
		t.Fatal("create preceded durable dispatch marker")
	}
	if err = s.CleanupCredentials(janitorContext(ctx), op.ID, 1, h.Revision()); !errors.Is(err, ErrUncertain) {
		t.Fatal("missing Job treated as safe compensation")
	}
	if len(c.revoked) != 0 {
		t.Fatal("possibly active workload lost credential")
	}
	assertPending(t, s, ctx, op.ID, 1)
}

func TestReviewObservationCannotAccessProviderWithoutJournal(t *testing.T) {
	c := &fixtureCredentials{}
	h, _ := recoveryHelm(t, c)
	op := Operation{Request: Request{ID: uuid.New(), Artifact: "sha256:" + strings.Repeat("a", 64)}, TargetRevision: h.Revision(), Attempts: []Attempt{{Number: 1, Kind: "execute"}, {Number: 2, Kind: "observe"}}, Credentials: []CredentialObligation{{Attempt: 1, ProviderBinding: h.Revision(), Phase: "preparing"}}}
	if _, err := h.Observe(context.Background(), op); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	if c.prepared != 0 || c.revoked != 0 {
		t.Fatalf("unjournaled observation reached provider: %+v", c)
	}
}
