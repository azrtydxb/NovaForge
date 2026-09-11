package runner_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/runner"
)

// TestPodExecutorCreatesIsolatedPod proves the isolated path actually builds a
// pod from the job's image rather than running anything on the runner host.
func TestPodExecutorCreatesIsolatedPod(t *testing.T) {
	cs := fake.NewSimpleClientset()
	px := &runner.PodExecutor{Client: cs, Namespace: "novaforge"}

	job := &civ1.ConnectResponse{
		JobId: "abc123", RunId: "run1",
		Image:        "alpine:3.21",
		RunCmd:       "echo hello",
		RepoCloneUrl: "http://git/x.git",
		CommitSha:    "deadbeef",
	}

	// The fake clientset never moves the pod out of Pending, so run in the
	// background and assert on what was created.
	go func() {
		logs := make(chan string, 8)
		go func() {
			for range logs {
			}
		}()
		_, _ = px.Run(context.Background(), job, logs)
	}()

	var pod *corev1.Pod
	for i := 0; i < 100 && pod == nil; i++ {
		list, err := cs.CoreV1().Pods("novaforge").List(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(list.Items) > 0 {
			pod = &list.Items[0]
		}
	}
	if pod == nil {
		t.Fatal("no job pod was created")
	}
	if got := pod.Spec.Containers[0].Image; got != "alpine:3.21" {
		t.Fatalf("want the job's image, got %q", got)
	}
	if pod.Labels["novaforge.io/job-id"] != "abc123" {
		t.Fatalf("pod is not labelled with its job: %v", pod.Labels)
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatalf("a job pod must not restart, got %v", pod.Spec.RestartPolicy)
	}
	// The clone must happen inside the pod: cloning on the host would place
	// repository content on the runner, which is what isolation avoids.
	if args := pod.Spec.Containers[0].Args[0]; !contains(args, "git clone") {
		t.Fatalf("clone not performed inside the pod: %q", args)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
