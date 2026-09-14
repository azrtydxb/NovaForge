package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

// Root is where a workspace's files live inside its pod.
const Root = "/workspace"

// DefaultImage is the workspace image when a repository declares none. It
// carries the Go toolchain, git, tar and a shell: the workspace is where an
// agent runs the repository's tests before committing.
const DefaultImage = "golang:1.27"

// ErrNoExec reports a Provisioner built without a REST config, which can
// create workspaces but not act inside them.
var ErrNoExec = errors.New("workspace: this provisioner cannot exec into pods (no REST config)")

// WithRESTConfig lets the Provisioner exec into the pods it creates. The
// clientset alone can create a pod but not stream a command into it.
func (p *Provisioner) WithRESTConfig(cfg *rest.Config) *Provisioner {
	p.restConfig = cfg
	return p
}

// WaitReady blocks until runID's workspace pod is running, or fails when it
// cannot start (an image that will not pull, a pod that exited) or timeout
// passes. An agent handed a workspace that never started would see every
// workspace tool fail with an opaque exec error.
func (p *Provisioner) WaitReady(ctx context.Context, runID uuid.UUID, timeout time.Duration) error {
	ns := namespaceFor(runID)
	var last string
	err := wait.PollUntilContextTimeout(ctx, time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		pod, err := p.client.CoreV1().Pods(ns).Get(ctx, podName, metav1GetOptions)
		if err != nil {
			last = err.Error()
			return false, nil
		}
		switch pod.Status.Phase {
		case corev1.PodRunning:
			return true, nil
		case corev1.PodSucceeded, corev1.PodFailed:
			return false, fmt.Errorf("workspace pod %s/%s exited (%s) before the run could use it", ns, podName, pod.Status.Phase)
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if w := cs.State.Waiting; w != nil {
				last = w.Reason + ": " + w.Message
				switch w.Reason {
				case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CreateContainerConfigError":
					return false, fmt.Errorf("workspace pod %s/%s cannot start: %s", ns, podName, last)
				}
			}
		}
		return false, nil
	})
	if err != nil && last != "" {
		return fmt.Errorf("%w (last seen: %s)", err, last)
	}
	return err
}

// ExecResult is what a command run inside a workspace produced.
type ExecResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Exec runs command inside runID's workspace pod, feeding it stdin when
// non-nil. A command that ran and exited non-zero is a result, not an error:
// err is reserved for failing to run it at all.
func (p *Provisioner) Exec(ctx context.Context, runID uuid.UUID, command []string, stdin io.Reader) (ExecResult, error) {
	if p.restConfig == nil {
		return ExecResult{}, ErrNoExec
	}
	req := p.client.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespaceFor(runID)).
		Name(podName).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   command,
			Stdin:     stdin != nil,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	// WebSocket is the current exec protocol; SPDY is kept as the fallback for
	// an API server that refuses the upgrade.
	ws, err := remotecommand.NewWebSocketExecutor(p.restConfig, "GET", req.URL().String())
	if err != nil {
		return ExecResult{}, fmt.Errorf("workspace exec: %w", err)
	}
	spdy, err := remotecommand.NewSPDYExecutor(p.restConfig, "POST", req.URL())
	if err != nil {
		return ExecResult{}, fmt.Errorf("workspace exec: %w", err)
	}
	executor, err := remotecommand.NewFallbackExecutor(ws, spdy, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return ExecResult{}, fmt.Errorf("workspace exec: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: &stdout, Stderr: &stderr})
	res := ExecResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	var exitErr utilexec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitStatus()
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("workspace exec: %w", err)
	}
	return res, nil
}
