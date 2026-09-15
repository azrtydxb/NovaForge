package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
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
	Client kubernetes.Interface
	// RestConfig is needed for the exec API that copies artifacts out of a
	// finished job's pod; the typed client alone cannot stream.
	RestConfig *rest.Config
	Namespace  string
	// Timeout bounds the whole job, so a wedged pod cannot occupy a runner slot
	// indefinitely.
	Timeout time.Duration
	// OnArtifacts receives whatever the job declared, once, after it succeeds.
	OnArtifacts func(ctx context.Context, jobID string, artifacts []Artifact) error
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
	return &PodExecutor{Client: cs, RestConfig: cfg, Namespace: namespace, Timeout: 2 * time.Hour}, nil
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
	// 57 leaves room for the credentials Secret's "-creds" suffix inside
	// the 63 characters a name may have.
	if len(name) > 57 {
		name = name[:57]
	}

	script := job.GetRunCmd() + artifactCaptureScript(job.GetArtifactPaths())
	if url := job.GetRepoCloneUrl(); url != "" {
		// The clone happens inside the pod too: cloning on the host would put
		// repository content on the runner, which is what isolation avoids.
		script = fmt.Sprintf(
			"set -e\ngit clone %q /workspace/repo\ncd /workspace/repo\n"+
				"if [ -n %q ]; then git checkout %q; fi\n%s",
			url, job.GetCommitSha(), job.GetCommitSha(), script)
	}

	env := make([]corev1.EnvVar, 0, len(job.GetEnv()))
	for k, v := range job.GetEnv() {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}

	// Brokered credentials reach the job through a Secret the pod reads as
	// environment. Set as literal env they would sit in the pod spec, readable
	// by anyone who can get pods in this namespace and printed by every
	// describe. The Secret lives exactly as long as the pod.
	var envFrom []corev1.EnvFromSource
	if secretEnv := job.GetSecretEnv(); len(secretEnv) > 0 {
		for k := range secretEnv {
			if errs := validation.IsEnvVarName(k); len(errs) > 0 {
				return 0, fmt.Errorf("credential %q is not a valid environment variable name: %s", k, strings.Join(errs, "; "))
			}
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name + "-creds",
				Namespace: p.Namespace,
				Labels:    map[string]string{"novaforge.io/job-id": strings.ToLower(job.GetJobId())},
			},
			Type:       corev1.SecretTypeOpaque,
			StringData: secretEnv,
		}
		if _, err := p.Client.CoreV1().Secrets(p.Namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return 0, fmt.Errorf("create job credentials: %w", err)
		}
		defer func() {
			delCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = p.Client.CoreV1().Secrets(p.Namespace).Delete(delCtx, secret.Name, metav1.DeleteOptions{})
		}()
		envFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: secret.Name},
		}}}
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
				EnvFrom:    envFrom,
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

	// The job's own output carries its artifacts, so the stream is filtered
	// here rather than forwarded raw: a reader of the log should never see the
	// encoded blob.
	raw := make(chan string, 256)
	type filtered struct {
		arts []Artifact
		err  error
	}
	done := make(chan filtered, 1)
	go func() {
		arts, err := filterOutput(ctx, raw, logs)
		done <- filtered{arts, err}
	}()
	sent, err := p.streamLogs(ctx, created.Name, raw)
	if err != nil {
		close(raw)
		<-done
		return 0, err
	}

	code, err := p.waitForExit(ctx, created.Name)
	if err != nil {
		close(raw)
		<-done
		return code, err
	}

	// A followed log is not a complete one. When a node runs out of inotify
	// instances the kubelet ends the follow early and writes its own error
	// ("failed to create fsnotify watcher: too many open files") into the
	// stream as if the job had printed it; the job's last lines never arrived,
	// and neither did its artifacts. So once the job has exited its whole log
	// is read again, without following, and every line past what was already
	// forwarded is sent. The follow holds its latest line back for exactly this
	// reason: a line the kubelet invented is not in the job's real log.
	if full, ferr := p.fullLog(ctx, created.Name); ferr == nil {
		for _, line := range remainingLines(full, sent) {
			select {
			case raw <- line:
			case <-ctx.Done():
			}
		}
	} else {
		select {
		case raw <- "novaforge: the job's complete log could not be read, so it may be missing its last lines: " + ferr.Error():
		case <-ctx.Done():
		}
	}
	close(raw)
	out := <-done
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	arts, aerr := out.arts, out.err

	// Artifacts are kept only from a job that succeeded: a failed job's outputs
	// are usually half-written, and keeping them invites trusting them.
	if code == 0 && p.OnArtifacts != nil {
		if aerr != nil {
			logs <- "novaforge: reading artifacts failed: " + aerr.Error()
		} else if len(arts) > 0 {
			if uerr := p.OnArtifacts(ctx, job.GetJobId(), arts); uerr != nil {
				// Losing artifacts does not change whether the job passed.
				logs <- "novaforge: uploading artifacts failed: " + uerr.Error()
			}
		}
	}
	return code, nil
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

func (p *PodExecutor) streamLogs(ctx context.Context, podName string, logs chan<- string) (int, error) {
	// Wait for the container to start producing output before attaching.
	for {
		pod, err := p.Client.CoreV1().Pods(p.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return 0, fmt.Errorf("get job pod: %w", err)
		}
		switch pod.Status.Phase {
		case corev1.PodRunning, corev1.PodSucceeded, corev1.PodFailed:
			goto attach
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(time.Second):
		}
	}
attach:
	stream, err := p.Client.CoreV1().Pods(p.Namespace).
		GetLogs(podName, &corev1.PodLogOptions{Follow: true}).Stream(ctx)
	if err != nil {
		return 0, fmt.Errorf("stream job logs: %w", err)
	}
	defer stream.Close()
	return forwardHoldingLast(ctx, stream, logs)
}

// forwardHoldingLast forwards each line of r except the last one it has read,
// which is held until the next arrives. It returns how many lines it forwarded.
// The held line is the one a broken follow may have invented; the caller settles
// it against the job's complete log.
func forwardHoldingLast(ctx context.Context, r io.Reader, logs chan<- string) (int, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), logScannerMaxLine)
	sent := 0
	held, holding := "", false
	for sc.Scan() {
		if holding {
			select {
			case logs <- held:
				sent++
			case <-ctx.Done():
				return sent, ctx.Err()
			}
		}
		held, holding = sc.Text(), true
	}
	return sent, sc.Err()
}

// fullLog reads a finished job pod's entire log, without following.
func (p *PodExecutor) fullLog(ctx context.Context, podName string) ([]string, error) {
	stream, err := p.Client.CoreV1().Pods(p.Namespace).GetLogs(podName, &corev1.PodLogOptions{}).Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("read job log: %w", err)
	}
	defer stream.Close()
	var lines []string
	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 0, 64*1024), logScannerMaxLine)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

// remainingLines is the part of a job's complete log not yet forwarded.
func remainingLines(full []string, sent int) []string {
	if sent >= len(full) {
		return nil
	}
	return full[sent:]
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

// ForwardHoldingLastForTest and RemainingLinesForTest expose the log completion
// helpers to the package's external tests.
func ForwardHoldingLastForTest(ctx context.Context, r io.Reader, logs chan<- string) (int, error) {
	return forwardHoldingLast(ctx, r, logs)
}

// RemainingLinesForTest exposes remainingLines.
func RemainingLinesForTest(full []string, sent int) []string { return remainingLines(full, sent) }
