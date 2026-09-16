package runner_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/runner"
)

// TestJobLogIsReadableWhileTheJobRuns pins the liveness of a job's log: a line
// the job prints must reach the log while the job is still running. That is
// what the follow-based capture lost twice over: holding the stream's latest
// line back kept the only line of a print-then-sleep job until the job ended,
// so the live log stayed empty and the whole log arrived in one burst racing
// the status flip to success. A follow also had a defect of its own — a node
// out of inotify instances ends the follow early and writes its own error into
// the stream as if the job had printed it. Reading the log without following
// touches no watcher, so none of that can happen; the last line is settled by
// the settled read once the pod is terminal.
func TestJobLogIsReadableWhileTheJobRuns(t *testing.T) {
	cs := fake.NewSimpleClientset()
	px := &runner.PodExecutor{Client: cs, Namespace: "novaforge", Timeout: 20 * time.Second}
	job := &civ1.ConnectResponse{JobId: "live-log-1", RunId: "run-1", Image: "alpine:3.21", RunCmd: "true"}

	var mu sync.Mutex
	logBody := "compiling\n"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "nf-job-live-log-1", Namespace: "novaforge"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}

	// The job's log is the pods/log subresource, read through a reactor that
	// serves the body the job has printed so far; the pod itself is served by
	// the same reactor with its phase updated when the job is told to end.
	cs.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		ga, ok := action.(k8stesting.GenericActionImpl)
		if !ok || ga.GetSubresource() != "log" {
			return true, pod.DeepCopy(), nil
		}
		return true, &runtime.Unknown{Raw: []byte(logBody)}, nil
	})

	logs := make(chan string, 16)
	finished := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); <-finished })
	go func() {
		defer close(finished)
		_, _ = px.Run(ctx, job, logs)
	}()

	// The first line must arrive while the job still runs: the pod is still
	// Running here and Run has not returned.
	select {
	case got := <-logs:
		if got != "compiling" {
			t.Fatalf("first line = %q, want compiling", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a line the job printed did not reach the log while the job was running")
	}

	// Later polls must forward only newly completed lines, not stop at the
	// first newline or resend the prefix. An unfinished line waits for EOF.
	mu.Lock()
	logBody = "compiling\ntesting\npackaging\npartial"
	mu.Unlock()
	for _, want := range []string{"testing", "packaging"} {
		select {
		case got := <-logs:
			if got != want {
				t.Fatalf("live line = %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("subsequent line %q was not delivered while running", want)
		}
	}

	// The job ends with a final line lacking a newline.
	mu.Lock()
	logBody = "compiling\ntesting\npackaging\npartial last line"
	pod.Status.Phase = corev1.PodSucceeded
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
	}}
	mu.Unlock()

	var got []string
	deadline := time.After(10 * time.Second)
	for len(got) < 1 {
		select {
		case l, ok := <-logs:
			if !ok {
				t.Fatalf("the log closed before the job's last line arrived: %v", got)
			}
			got = append(got, l)
		case <-deadline:
			t.Fatalf("the job's last line never reached the log; lines so far: %v", got)
		}
	}
	<-finished
	if len(logs) != 0 {
		t.Fatalf("unexpected extra lines: %d", len(logs))
	}
	if strings.Join(append([]string{"compiling"}, got...), "|") != "compiling|partial last line" {
		t.Fatalf("forwarded %q, want the first and terminal lines exactly once", got)
	}
}
