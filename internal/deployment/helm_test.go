package deployment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type fixtureCredentials struct {
	prepared int
	revoked  int
}

func (c *fixtureCredentials) Prepare(_ context.Context, op Operation, _ string, _ time.Duration) (Credential, error) {
	c.prepared++
	return Credential{SecretName: CredentialSecretName(op), ExpiresAt: credentialDeadline(op)}, nil
}
func (c *fixtureCredentials) Revoke(context.Context, Operation, string) error {
	c.revoked++
	return nil
}

func helmConfig() HelmConfig {
	return HelmConfig{TargetClusterID: "fixture-cluster", ExecutionNamespace: "executor", TargetNamespace: "target", Release: "fixture", Image: "registry.internal/trusted@sha256:" + strings.Repeat("b", 64), ChartPath: "/charts/trusted.tgz", ArtifactValueKey: "image.digest", ServiceAccount: "executor-no-target-rights", CredentialPolicyRevision: "fixture-openbao-role-v1"}
}

func TestHelmExecutionFromApprovedRequestToControlledProcess(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	dir := t.TempDir()
	action := filepath.Join(dir, "action")
	script := `#!/bin/sh
if [ "$1" = status ]; then exit 1; fi
printf 'performed\n' >> "$ACTION_FILE"
while [ "$#" -gt 0 ]; do
 if [ "$1" = --description ]; then shift; binding="$1"; fi
 shift
done
printf '{"name":"fixture","namespace":"target","version":9,"info":{"status":"deployed","description":"%s"},"manifest":"SECRET-MUST-NOT-ESCAPE"}' "$binding"
`
	if err := os.WriteFile(filepath.Join(dir, "helm"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ACTION_FILE", action)
	var mu sync.Mutex
	var created *batchv1.Job
	var log bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/jobs") && r.Method == http.MethodPost {
			var job batchv1.Job
			if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if problems := validation.IsDNS1123Label(job.Name); len(problems) > 0 {
				t.Error(problems)
				w.WriteHeader(422)
				return
			}
			for key, value := range job.Labels {
				if len(validation.IsQualifiedName(key)) > 0 || len(validation.IsValidLabelValue(value)) > 0 {
					t.Error("invalid labels")
					w.WriteHeader(422)
					return
				}
			}
			spec := job.Spec.Template.Spec
			if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken || spec.SecurityContext == nil || !*spec.SecurityContext.RunAsNonRoot || len(spec.Containers) != 1 || spec.Containers[0].Command[0] != "/usr/local/bin/deployment-runner" {
				t.Error("unsafe executor workload")
				w.WriteHeader(422)
				return
			}
			if err := RunHelmJob(r.Context(), spec.Containers[0].Args, &log); err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			job.UID = types.UID(uuid.NewString())
			job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
			created = &job
			_ = json.NewEncoder(w).Encode(job)
			return
		}
		if strings.Contains(r.URL.Path, "/jobs/") {
			if created == nil {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","reason":"NotFound","code":404}`))
				return
			}
			_ = json.NewEncoder(w).Encode(created)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/pods") {
			_ = json.NewEncoder(w).Encode(corev1.PodList{Items: []corev1.Pod{evidencePod(created)}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/log") {
			_, _ = w.Write(log.Bytes())
			return
		}
		t.Errorf("unexpected fixture API request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	creds := &fixtureCredentials{}
	executor, err := NewHelmExecutor(client, helmConfig(), creds)
	if err != nil {
		t.Fatal(err)
	}
	s.targets[req.Target].Executor = executor
	s.targets[req.Target].Revision = executor.Revision()
	s.targets[req.Target].Destination = executor.Destination()
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	got, err := s.Execute(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateSucceeded || got.Attempts[0].Result.ExternalID != "fixture/9" {
		t.Fatalf("bad result: %+v", got)
	}
	if _, err = s.Execute(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(action)
	if err != nil || string(data) != "performed\n" {
		t.Fatalf("external side effect not exactly once: %q %v", data, err)
	}
	if strings.Contains(log.String(), "SECRET") {
		t.Fatal("raw Helm manifest leaked")
	}
	// Passive administrative recovery reads only an existing job and logs.
	recovery := got
	recovery.Attempts = append(recovery.Attempts, Attempt{Number: 2, Kind: "recover"})
	if _, err := executor.ObserveExisting(admin, recovery); err != nil {
		t.Fatal(err)
	}
	if creds.prepared != 1 || creds.revoked != 1 {
		t.Fatalf("passive recovery changed credential lifecycle: %+v", creds)
	}
}

func TestHelmConfigurationAndJobBindingCannotEscalate(t *testing.T) {
	client, err := kubernetes.NewForConfig(&rest.Config{Host: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*HelmConfig){func(c *HelmConfig) { c.Image = "trusted:latest" }, func(c *HelmConfig) { c.ChartPath = "/charts/../evil.tgz" }, func(c *HelmConfig) { c.ArtifactValueKey = "image.digest,privileged=true" }, func(c *HelmConfig) { c.TargetNamespace = "../production" }} {
		config := helmConfig()
		mutate(&config)
		if _, err := NewHelmExecutor(client, config, &fixtureCredentials{}); err == nil {
			t.Fatal("unsafe config accepted")
		}
	}
	h, err := NewHelmExecutor(client, helmConfig(), &fixtureCredentials{})
	if err != nil {
		t.Fatal(err)
	}
	op := Operation{Request: Request{ID: uuid.New(), Artifact: "sha256:" + strings.Repeat("a", 64)}, TargetRevision: h.Revision(), Attempts: []Attempt{{Number: 1, Kind: "execute"}}}
	job := h.job(op, "execute", "fixture-job", CredentialSecretName(op))
	if !h.matches(job, op, "execute") {
		t.Fatal("own job rejected")
	}
	job.Spec.Template.Spec.Containers[0].Args = append(job.Spec.Template.Spec.Containers[0].Args, "--take-ownership")
	if h.matches(job, op, "execute") {
		t.Fatal("mutated command accepted")
	}
	job = h.job(op, "execute", "fixture-job", CredentialSecretName(op))
	job.Spec.Template.Spec.HostNetwork = true
	if h.matches(job, op, "execute") {
		t.Fatal("privileged namespace accepted")
	}
	job = h.job(op, "execute", "fixture-job", CredentialSecretName(op))
	job.Spec.Template.Spec.AutomountServiceAccountToken = nil
	if h.matches(job, op, "execute") {
		t.Fatal("implicit controller token accepted")
	}
	job = h.job(op, "execute", "fixture-job", CredentialSecretName(op))
	job.Spec.ActiveDeadlineSeconds = nil
	if h.matches(job, op, "execute") {
		t.Fatal("unbounded execution job accepted")
	}
	job = h.job(op, "execute", "fixture-job", CredentialSecretName(op))
	parallel := int32(2)
	job.Spec.Parallelism = &parallel
	if h.matches(job, op, "execute") {
		t.Fatal("parallel duplicate execution accepted")
	}

}

func TestUncertainDeploymentRequiresReadOnlyReconciliation(t *testing.T) {
	s, ctx, admin, req, _ := fixture(t)
	op, err := s.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, op)
	// Simulate a process dying after its durable start, before its result write.
	if _, err = crashStart(ctx, s, op); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Retry(ctx, op.ID); !errors.Is(err, ErrUncertain) {
		t.Fatalf("ambiguous attempt blindly retried: %v", err)
	}
	next := req
	next.ID = uuid.New()
	nextOp, err := s.Request(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	approve(t, s, admin, nextOp)
	if _, err = s.Execute(ctx, nextOp.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("unknown prior operation bypassed: %v", err)
	}
	got, err := s.Reconcile(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateSucceeded || len(got.Attempts) != 2 || got.Attempts[0].State != StateUncertain || got.Attempts[1].Kind != "observe" {
		t.Fatalf("recovery erased evidence: %+v", got)
	}
}
