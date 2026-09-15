package runner_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/fake"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/runner"
)

// TestPodReceivesCredentialsFromASecret pins how a brokered credential reaches
// a job: through a Kubernetes Secret the pod reads as environment, never as a
// literal in the pod spec, where anyone allowed to read pods in the job
// namespace — and every event and describe — would see it. The Secret's name
// is checked against the API server's own validator, not the fake's
// tolerance, because the fake accepts names the cluster refuses.
func TestPodReceivesCredentialsFromASecret(t *testing.T) {
	cs := fake.NewSimpleClientset()
	px := &runner.PodExecutor{Client: cs, Namespace: "novaforge"}
	value := "nf-" + uuid.NewString()
	job := &civ1.ConnectResponse{
		JobId:     strings.ToUpper(uuid.NewString()),
		RunId:     "run1",
		RunCmd:    "deploy.sh",
		Env:       map[string]string{"MODE": "fast"},
		SecretEnv: map[string]string{"DEPLOY_TOKEN": value},
	}
	go func() {
		logs := make(chan string, 8)
		go func() {
			for range logs {
			}
		}()
		_, _ = px.Run(context.Background(), job, logs)
	}()

	var pods []corev1.Pod
	for i := 0; i < 200 && len(pods) == 0; i++ {
		list, _ := cs.CoreV1().Pods("novaforge").List(context.Background(), metav1.ListOptions{})
		pods = list.Items
		if len(pods) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if len(pods) != 1 {
		t.Fatalf("want one job pod, got %d", len(pods))
	}
	c := pods[0].Spec.Containers[0]
	for _, e := range c.Env {
		if strings.Contains(e.Value, value) {
			t.Fatalf("the credential is a literal in the pod spec (env %s)", e.Name)
		}
	}
	if strings.Contains(strings.Join(c.Args, " "), value) {
		t.Fatal("the credential is in the pod's command line")
	}
	if len(c.EnvFrom) != 1 || c.EnvFrom[0].SecretRef == nil {
		t.Fatalf("pod does not read its credentials from a Secret: %+v", c.EnvFrom)
	}
	name := c.EnvFrom[0].SecretRef.Name
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		t.Fatalf("Secret name %q would be refused by the API server: %v", name, errs)
	}
	sec, err := cs.CoreV1().Secrets("novaforge").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the Secret the pod reads does not exist: %v", err)
	}
	if sec.StringData["DEPLOY_TOKEN"] != value && string(sec.Data["DEPLOY_TOKEN"]) != value {
		t.Fatalf("the Secret does not carry the credential under its variable name: %+v", sec)
	}
}
