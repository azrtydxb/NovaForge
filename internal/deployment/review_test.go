package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type brokenObservationCredentials struct{ invalid bool }

func (c brokenObservationCredentials) Prepare(context.Context, Operation, string, time.Duration) (Credential, error) {
	if c.invalid {
		return Credential{SecretName: "wrong", ExpiresAt: time.Now()}, nil
	}
	return Credential{}, errors.New("provider unavailable")
}
func (brokenObservationCredentials) Revoke(context.Context, Operation, string) error { return nil }

func TestReviewObservationFailurePreservesExclusion(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "issuance", true: "invalid"}[invalid], func(t *testing.T) {
			s, ctx, admin, req, _ := fixture(t)
			var active *batchv1.Job
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if active != nil && strings.HasSuffix(r.URL.Path, "/"+active.Name) {
					_ = json.NewEncoder(w).Encode(active)
					return
				}
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","reason":"NotFound","code":404}`))
			}))
			defer server.Close()
			client, _ := kubernetes.NewForConfig(&rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
			h, err := NewHelmExecutor(client, helmConfig(), brokenObservationCredentials{invalid})
			if err != nil {
				t.Fatal(err)
			}
			s.targets[req.Target].Executor = h
			s.targets[req.Target].Revision = h.Revision()
			s.targets[req.Target].Destination = h.Destination()
			op, err := s.Request(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			approve(t, s, admin, op)
			attempt := op
			attempt.Attempts = []Attempt{{Number: 1, Kind: "execute"}}
			active = h.job(attempt, "execute", "nf-deploy-"+op.ID.String()+"-1", CredentialSecretName(attempt))
			call, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			if _, err = s.Execute(call, op.ID); !errors.Is(err, ErrUncertain) {
				t.Fatalf("active delivery: %v", err)
			}
			got, err := s.Reconcile(ctx, op.ID)
			if err == nil || got.State != StateUncertain {
				t.Errorf("observation failure cleared uncertainty: state=%s err=%v", got.State, err)
			}
			if _, err = s.Retry(ctx, op.ID); !errors.Is(err, ErrUncertain) {
				t.Errorf("retry accepted after observation failure: %v", err)
			}
			req.ID = uuid.New()
			next, err := s.Request(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			approve(t, s, admin, next)
			if _, err = s.Execute(ctx, next.ID); !errors.Is(err, ErrConflict) && !errors.Is(err, ErrBusy) {
				t.Errorf("another operation bypassed uncertainty: %v", err)
			}
		})
	}
}

func TestReviewLockSessionLoss(t *testing.T) {
	t.Run("before start", func(t *testing.T) {
		s, ctx, admin, req, exec := fixture(t)
		op, err := s.Request(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		approve(t, s, admin, op)
		// Only this random fixture database's advisory-lock backend is terminated.
		_, err = s.pool.Exec(ctx, `CREATE FUNCTION deployment.kill_lock() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND database=(SELECT oid FROM pg_database WHERE datname=current_database()); RETURN NEW; END $$; CREATE TRIGGER kill_lock BEFORE UPDATE ON deployment.operations FOR EACH ROW EXECUTE FUNCTION deployment.kill_lock()`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Execute(ctx, op.ID)
		if err == nil || exec.calls.Load() != 0 {
			t.Errorf("lost lock authorized dispatch: calls=%d err=%v", exec.calls.Load(), err)
		}
		_, err = s.pool.Exec(ctx, `DROP TRIGGER kill_lock ON deployment.operations`)
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("during execution", func(t *testing.T) {
		s, ctx, admin, req, exec := fixture(t)
		op, err := s.Request(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		approve(t, s, admin, op)
		exec.started = make(chan struct{})
		exec.release = make(chan struct{})
		done := make(chan error, 1)
		go func() { _, err := s.Execute(ctx, op.ID); done <- err }()
		<-exec.started
		var killed bool
		err = s.pool.QueryRow(ctx, `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&killed)
		if err != nil || !killed {
			close(exec.release)
			<-done
			t.Fatalf("terminate owned lock: %v", err)
		}
		close(exec.release)
		err = <-done
		if !errors.Is(err, ErrUncertain) {
			t.Errorf("stale finalization accepted: %v", err)
		}
		got, err := s.Get(ctx, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != StateRunning && got.State != StateUncertain {
			t.Errorf("stale worker finalized: %s", got.State)
		}
		req.ID = uuid.New()
		next, err := s.Request(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		approve(t, s, admin, next)
		// Avoid a second fixture executor channel close if the broken guard dispatches.
		exec.started = nil
		if _, err = s.Execute(ctx, next.ID); !errors.Is(err, ErrConflict) && !errors.Is(err, ErrBusy) {
			t.Errorf("lost-session action stopped excluding target: %v", err)
		}
	})
}

func TestReviewTargetAliases(t *testing.T) {
	s, ctx, admin, req, exec := fixture(t)
	first := *s.targets[req.Target]
	alias := first
	alias.Name = "alias"
	var err error
	s, err = NewService(s.pool, s.approvals, []Target{first, alias}, s.resolveRun)
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	exec.started = make(chan struct{})
	exec.release = make(chan struct{})
	call, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, e := s.Execute(call, op.ID); done <- e }()
	<-exec.started
	req.ID = uuid.New()
	req.Target = "alias"
	next, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, next)
	// A separate executor avoids fixture channel panic if aliases bypass the lock.
	aliasExec := &fixtureExecutor{}
	s.targets["alias"].Executor = aliasExec
	if _, err = s.Execute(ctx, next.ID); !errors.Is(err, ErrBusy) && !errors.Is(err, ErrConflict) {
		t.Errorf("concurrent alias dispatched: %v", err)
	}
	cancel()
	<-done
	s.targets["alias"].Revision = "fixture-v2"
	req.ID = uuid.New()
	next, err = s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, next)
	if _, err = s.Execute(ctx, next.ID); !errors.Is(err, ErrBusy) && !errors.Is(err, ErrConflict) {
		t.Errorf("revision alias bypassed uncertainty: %v", err)
	}
	if aliasExec.calls.Load() != 0 {
		t.Errorf("alias calls=%d", aliasExec.calls.Load())
	}
}

func evidencePod(job *batchv1.Job) corev1.Pod {
	yes := true
	spec := *job.Spec.Template.Spec.DeepCopy()
	spec.DNSPolicy = corev1.DNSClusterFirst
	spec.SchedulerName = "default-scheduler"
	grace := int64(30)
	spec.TerminationGracePeriodSeconds = &grace
	spec.Containers[0].TerminationMessagePath = "/dev/termination-log"
	spec.Containers[0].TerminationMessagePolicy = corev1.TerminationMessageReadFile
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "executor-pod", Namespace: job.Namespace, OwnerReferences: []metav1.OwnerReference{{UID: job.UID, Name: job.Name, APIVersion: "batch/v1", Kind: "Job", Controller: &yes}}}, Spec: spec, Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{Name: "helm", Image: spec.Containers[0].Image, ImageID: "containerd://" + spec.Containers[0].Image, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}}}
}
func TestReviewEvidencePodContract(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*corev1.Pod)
	}{
		{"valid defaults", func(*corev1.Pod) {}},
		{"runtime class", func(p *corev1.Pod) { v := "untrusted"; p.Spec.RuntimeClassName = &v }},
		{"shared processes", func(p *corev1.Pod) { v := true; p.Spec.ShareProcessNamespace = &v }},
		{"dns", func(p *corev1.Pod) { p.Spec.DNSPolicy = corev1.DNSNone }},
		{"credential source", func(p *corev1.Pod) { p.Spec.Volumes[0].Secret.SecretName = "other-secret" }},
		{"image", func(p *corev1.Pod) { p.Spec.Containers[0].Image = "evil:latest" }},
		{"command", func(p *corev1.Pod) { p.Spec.Containers[0].Command = []string{"/bin/sh"} }},
		{"mount", func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[0].MountPath = "/elsewhere" }},
		{"security", func(p *corev1.Pod) { yes := true; p.Spec.Containers[0].SecurityContext.Privileged = &yes }},
		{"image identity", func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].ImageID = "containerd://sha256:" + strings.Repeat("c", 64)
		}},
		{"termination", func(p *corev1.Pod) { p.Status.ContainerStatuses[0].State.Terminated.ExitCode = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var pod corev1.Pod
			var proof HelmEvidence
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/pods") {
					_ = json.NewEncoder(w).Encode(corev1.PodList{Items: []corev1.Pod{pod}})
				} else {
					_ = json.NewEncoder(w).Encode(proof)
				}
			}))
			defer server.Close()
			client, _ := kubernetes.NewForConfig(&rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
			h, err := NewHelmExecutor(client, helmConfig(), &fixtureCredentials{})
			if err != nil {
				t.Fatal(err)
			}
			op := Operation{Request: Request{ID: uuid.New(), Artifact: "sha256:" + strings.Repeat("a", 64)}, TargetRevision: h.Revision(), Attempts: []Attempt{{Number: 1, Kind: "execute"}}}
			job := h.job(op, "execute", "fixture-job", CredentialSecretName(op))
			job.UID = types.UID(uuid.NewString())
			pod = evidencePod(job)
			tc.mutate(&pod)
			proof = HelmEvidence{Binding: helmBinding(op), Release: h.config.Release, Namespace: h.config.TargetNamespace, Revision: 1, State: StateSucceeded}
			_, err = h.evidence(context.Background(), job, op)
			if tc.name == "valid defaults" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrUncertain) {
				t.Fatalf("substituted pod evidence accepted: %v", err)
			}
		})
	}
}

func TestReviewCanonicalHelmDestinationAndOrgOwnership(t *testing.T) {
	s, ctx, _, req, _ := fixture(t)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	config := helmConfig()
	h, err := NewHelmExecutor(client, config, &fixtureCredentials{})
	if err != nil {
		t.Fatal(err)
	}
	config.Image = "other/trusted@sha256:" + strings.Repeat("c", 64)
	config.ChartPath = "/charts/v2.tgz"
	config.CredentialPolicyRevision = "role-v2"
	revised, err := NewHelmExecutor(client, config, &fixtureCredentials{})
	if err != nil {
		t.Fatal(err)
	}
	if h.Destination() != revised.Destination() || h.Revision() == revised.Revision() {
		t.Fatal("destination identity follows mutable policy")
	}
	first := *s.targets[req.Target]
	first.Executor = h
	first.Revision = h.Revision()
	first.Destination = ""
	alias := first
	alias.Name = "helm-alias"
	alias.Executor = revised
	alias.Revision = revised.Revision()
	s, err = NewService(s.pool, s.approvals, []Target{first, alias}, s.resolveRun)
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if op.Destination != h.Destination() {
		t.Fatal("destination not stored")
	}
	if _, err = crashStart(ctx, s, op); err != nil {
		t.Fatal(err)
	}
	req.ID = uuid.New()
	req.Target = alias.Name
	next, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = crashStart(ctx, s, next); !errors.Is(err, ErrConflict) {
		t.Fatalf("Helm alias bypassed durable exclusion: %v", err)
	}
	// A separate service replica/config cannot map another organization to the
	// same destination; no foreign operation fields appear in the response.
	scope, _ := authz.FromContext(ctx)
	scope.OrgID = uuid.New()
	foreignCtx := authz.WithScope(context.Background(), scope)
	foreign := alias
	foreign.OrgID = scope.OrgID
	resolve := s.resolveRun
	other, err := NewService(s.pool, s.approvals, []Target{foreign}, func(c context.Context, id uuid.UUID) (Run, error) {
		r, e := resolve(c, id)
		r.OrgID = scope.OrgID
		r.Grant.OrgID = scope.OrgID
		return r, e
	})
	if err != nil {
		t.Fatal(err)
	}
	req.ID = uuid.New()
	got, err := other.Request(foreignCtx, req)
	if !errors.Is(err, ErrConflict) || got.ID != uuid.Nil {
		t.Fatalf("foreign destination mapping accepted or leaked: id=%s err=%v", got.ID, err)
	}
	if _, err = other.Get(foreignCtx, op.ID); err == nil {
		t.Fatal("foreign evidence readable")
	}
}
