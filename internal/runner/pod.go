package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
)

// ErrLocalExecutionNotPermitted is returned when no container runtime is
// configured and local execution has not been explicitly enabled.
//
// A CI job runs whatever the repository's workflow file says, so the command is
// attacker-controlled by construction. The defence is isolation, not input
// validation: jobs run in a pod. Running them on the runner host is a
// development convenience and must be opted into deliberately.
var ErrLocalExecutionNotPermitted = errors.New(
	"refusing to run a job command on the runner host: no Kubernetes client is " +
		"configured and NOVAFORGE_ALLOW_LOCAL_EXEC is not set")

// PodExecutor runs each job in its own Kubernetes pod, in the namespace the
// runner is given. The pod is deleted when the job finishes.
type PodExecutor struct {
	Client    kubernetes.Interface
	Namespace string
	// Timeout bounds the whole job, so a wedged pod cannot occupy a runner slot
	// indefinitely.
	Timeout time.Duration
}

// NewPodExecutorFromCluster builds an executor from the in-cluster service
// account. It returns nil, nil when the process is not running in a cluster,
// which is how a developer workstation is detected.
func NewPodExecutorFromCluster(namespace string) (*PodExecutor, error) {
	cfg, err := rest.InClusterConfig()
	if errors.Is(err, rest.ErrNotInCluster) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("in-cluster config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}
	return &PodExecutor{Client: cs, Namespace: namespace, Timeout: 2 * time.Hour}, nil
}

// LocalExecutionAllowed reports whether running job commands directly on the
// runner host has been explicitly enabled.
func LocalExecutionAllowed() bool {
	return os.Getenv("NOVAFORGE_ALLOW_LOCAL_EXEC") == "1"
}

// Run executes the job in a pod and streams its output into logs.
func (p *PodExecutor) Run(ctx context.Context, job *civ1.ConnectResponse, logs chan<- string) (int, error) {
	if p.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.Timeout)
		defer cancel()
	}

	image := job.GetImage()
	if image == "" {
		image = defaultJobImage()
	}
	name := "nf-job-" + strings.ToLower(job.GetJobId())
	if len(name) > 63 {
		name = name[:63]
	}

	script := job.GetRunCmd()
	if url := job.GetRepoCloneUrl(); url != "" {
		// The clone happens inside the pod too: cloning on the host would put
		// repository content on the runner, which is what isolation avoids.
		script = fmt.Sprintf(
			"set -e\ngit clone %q /workspace/repo\ncd /workspace/repo\n"+
				"if [ -n %q ]; then git checkout %q; fi\n%s",
			url, job.GetCommitSha(), job.GetCommitSha(), job.GetRunCmd())
	}

	env := make([]corev1.EnvVar, 0, len(job.GetEnv()))
	for k, v := range job.GetEnv() {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: p.Namespace,
			Labels: map[string]string{
				"novaforge.io/job-id": job.GetJobId(),
				"novaforge.io/run-id": job.GetRunId(),
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:       "job",
				Image:      image,
				Command:    []string{"/bin/sh", "-c"},
				Args:       []string{script},
				Env:        env,
				WorkingDir: "/workspace",
				VolumeMounts: []corev1.VolumeMount{
					{Name: "workspace", MountPath: "/workspace"},
				},
			}},
			Volumes: []corev1.Volume{{
				Name:         "workspace",
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			}},
		},
	}

	created, err := p.Client.CoreV1().Pods(p.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return 0, fmt.Errorf("create job pod: %w", err)
	}
	defer func() {
		delCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = p.Client.CoreV1().Pods(p.Namespace).Delete(delCtx, created.Name, metav1.DeleteOptions{})
	}()

	if err := p.streamLogs(ctx, created.Name, logs); err != nil {
		return 0, err
	}
	return p.waitForExit(ctx, created.Name)
}

// defaultJobImage is used when a workflow job names no image. It must contain
// git, because a job that checks out the repository runs the clone inside the
// pod — a bare base image exits 127 on the first line for a reason that reads
// like a user error rather than a missing dependency.
func defaultJobImage() string {
	if v := os.Getenv("CI_DEFAULT_JOB_IMAGE"); v != "" {
		return v
	}
	return "debian:13-slim"
}

func (p *PodExecutor) streamLogs(ctx context.Context, podName string, logs chan<- string) error {
	// Wait for the container to start producing output before attaching.
	for {
		pod, err := p.Client.CoreV1().Pods(p.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get job pod: %w", err)
		}
		switch pod.Status.Phase {
		case corev1.PodRunning, corev1.PodSucceeded, corev1.PodFailed:
			goto attach
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
attach:
	stream, err := p.Client.CoreV1().Pods(p.Namespace).
		GetLogs(podName, &corev1.PodLogOptions{Follow: true}).Stream(ctx)
	if err != nil {
		return fmt.Errorf("stream job logs: %w", err)
	}
	defer stream.Close()

	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 0, 64*1024), logScannerMaxLine)
	for sc.Scan() {
		select {
		case logs <- sc.Text():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return sc.Err()
}

func (p *PodExecutor) waitForExit(ctx context.Context, podName string) (int, error) {
	for {
		pod, err := p.Client.CoreV1().Pods(p.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return 0, fmt.Errorf("get job pod: %w", err)
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if t := cs.State.Terminated; t != nil {
				return int(t.ExitCode), nil
			}
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			return 0, nil
		case corev1.PodFailed:
			return 1, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
