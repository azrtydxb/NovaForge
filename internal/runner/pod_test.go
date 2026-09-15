package runner_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

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
			break
		}
		// The pod is created on another goroutine; a hundred lists with no
		// pause can all finish before it is scheduled, which failed this test
		// intermittently under a loaded machine.
		time.Sleep(20 * time.Millisecond)
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

// TestDefaultJobImageIsConfigurable pins why this exists: a workflow job that
// names no image still has to be able to clone, and a base image without git
// exits 127 on the first line — which reads like a broken workflow rather than
// a missing dependency.
func TestDefaultJobImageIsConfigurable(t *testing.T) {
	t.Setenv("CI_DEFAULT_JOB_IMAGE", "registry.example/novaforge/runner:abc123")

	cs := fake.NewSimpleClientset()
	px := &runner.PodExecutor{Client: cs, Namespace: "novaforge"}
	job := &civ1.ConnectResponse{JobId: "nodefault", RunId: "r1", RunCmd: "true"}

	go func() {
		logs := make(chan string, 4)
		go func() {
			for range logs {
			}
		}()
		_, _ = px.Run(context.Background(), job, logs)
	}()

	var pod *corev1.Pod
	for i := 0; i < 200 && pod == nil; i++ {
		list, _ := cs.CoreV1().Pods("novaforge").List(context.Background(), metav1.ListOptions{})
		if len(list.Items) > 0 {
			pod = &list.Items[0]
			break
		}
		time.Sleep(10 * time.Millisecond) // see TestPodExecutorCreatesIsolatedPod
	}
	if pod == nil {
		t.Fatal("no job pod was created")
	}
	if got := pod.Spec.Containers[0].Image; got != "registry.example/novaforge/runner:abc123" {
		t.Fatalf("want the configured default image, got %q", got)
	}
}

// TestArtifactsTravelThroughTheJobsOwnOutput pins why this is not done with
// kubectl-style exec: by the time a job has finished its container has
// terminated and there is nothing left to exec into, so the job captures its
// own declared outputs while it is still alive.
func TestArtifactsRoundTripThroughOutput(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("artifact body\n")
	if err := tw.WriteHeader(&tar.Header{
		Name: "out/report.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	tw.Write(body)
	tw.Close()

	lines := []string{
		"hello from novaforge ci",
		"::novaforge-artifacts-begin::",
		base64.StdEncoding.EncodeToString(buf.Bytes()),
		"::novaforge-artifacts-end::",
	}
	arts, clean, err := runner.ExtractArtifactsForTest(lines)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(arts) != 1 || arts[0].Name != "report.txt" || string(arts[0].Content) != "artifact body\n" {
		t.Fatalf("artifact not recovered: %+v", arts)
	}
	// The encoded blob must not reach whoever reads the log.
	for _, l := range clean {
		if strings.Contains(l, "novaforge-artifacts") || len(l) > 200 {
			t.Fatalf("payload leaked into the log: %q", l)
		}
	}
	if len(clean) != 1 || clean[0] != "hello from novaforge ci" {
		t.Fatalf("job output not preserved: %v", clean)
	}
}

func TestNoArtifactsDeclaredEmitsNoCaptureScript(t *testing.T) {
	if s := runner.ArtifactCaptureScriptForTest(nil); s != "" {
		t.Fatalf("a job declaring no artifacts must emit no capture script, got %q", s)
	}
	if s := runner.ArtifactCaptureScriptForTest([]string{"out/x"}); !strings.Contains(s, "tar cf -") {
		t.Fatalf("capture script does not archive: %q", s)
	}
}
