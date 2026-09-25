package gates

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/analysis"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/workspace"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

const sandboxOutputLimit = 8 << 20
const sandboxSourceLimit = 32 << 20

var immutableAnalysisImage = regexp.MustCompile(`^[^\s@]+@sha256:[a-f0-9]{64}$`)
var gateRevision = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)

// AnalysisSandbox executes repository tools only in disposable, network-denied
// pods. The image is operator-owned, never taken from repository configuration.
// No local execution fallback exists, including on Kubernetes failure.
type AnalysisSandbox struct {
	client kubernetes.Interface
	config *rest.Config
	image  string
}

func NewAnalysisSandbox(client kubernetes.Interface, config *rest.Config, image string) (*AnalysisSandbox, error) {
	if client == nil || config == nil || !immutableAnalysisImage.MatchString(image) {
		return nil, fmt.Errorf("analysis sandbox requires Kubernetes and a digest-pinned image")
	}
	return &AnalysisSandbox{client: client, config: rest.CopyConfig(config), image: image}, nil
}

func (s *AnalysisSandbox) bind(ctx context.Context, dir string, runID uuid.UUID, head RunHead) (analysis.Exec, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil || scope.OrgID != head.OrgID || runID == uuid.Nil || head.RepoID == uuid.Nil || !gateRevision.MatchString(head.HeadSHA) {
		return nil, fmt.Errorf("analysis sandbox requires scoped immutable revision identity")
	}
	snapshot, err := gateSnapshot(dir)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(snapshot)
	return func(ctx context.Context, workdir, name string, args ...string) ([]byte, int, error) {
		if workdir != dir {
			return nil, 0, fmt.Errorf("analysis workspace mismatch")
		}
		if err := authz.RequireOrg(ctx, head.OrgID); err != nil {
			return nil, 0, err
		}
		return s.execute(ctx, head, runID, hex.EncodeToString(digest[:]), snapshot, dir, name, args)
	}, nil
}

// Only regular snapshot bytes cross the boundary; no host path is mounted.
func gateSnapshot(dir string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	total, count := int64(0), 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported snapshot entry %s", path)
		}
		total += info.Size()
		count++
		if total > sandboxSourceLimit || count > 10000 {
			return fmt.Errorf("analysis snapshot exceeds bounded input")
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel), Mode: 0644, Size: info.Size()}); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.CopyN(tw, f, info.Size())
		return err
	})
	if err != nil {
		return nil, err
	}
	if err = tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gateSandboxPod(ns, image string, labels, annotations map[string]string) *corev1.Pod {
	uid := int64(65532)
	deadline := int64(600)
	zero := int64(0)
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "analysis", Namespace: ns, Labels: labels, Annotations: annotations}, Spec: corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: &deadline, TerminationGracePeriodSeconds: &zero,
		AutomountServiceAccountToken: new(false), EnableServiceLinks: new(false),
		SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: new(true), RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Containers: []corev1.Container{{Name: "analysis", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
			Command: []string{"/bin/sh", "-c", "trap 'exit 0' TERM; sleep 600 & wait"}, WorkingDir: workspace.Root,
			SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: new(false), ReadOnlyRootFilesystem: new(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			Env:             []corev1.EnvVar{{Name: "HOME", Value: "/tmp"}, {Name: "GOCACHE", Value: "/tmp/go-build"}, {Name: "GOMODCACHE", Value: "/tmp/go-mod"}, {Name: "GOTOOLCHAIN", Value: "local"}, {Name: "GOPROXY", Value: "off"}, {Name: "GOSUMDB", Value: "off"}, {Name: "GOTELEMETRY", Value: "off"}},
			Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("1Gi"), corev1.ResourceEphemeralStorage: resource.MustParse("2Gi")}},
			VolumeMounts:    []corev1.VolumeMount{{Name: "source", MountPath: workspace.Root}, {Name: "tmp", MountPath: "/tmp"}},
		}}, Volumes: []corev1.Volume{{Name: "source", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: resource.NewQuantity(1<<30, resource.BinarySI)}}}, {Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: resource.NewQuantity(1<<30, resource.BinarySI)}}}},
	}}
}

func (s *AnalysisSandbox) execute(ctx context.Context, head RunHead, runID uuid.UUID, digest string, snapshot []byte, dir, name string, args []string) (out []byte, exit int, err error) {
	ctx, cancel := context.WithTimeout(ctx, 9*time.Minute)
	defer cancel()
	ns := "nf-gate-" + uuid.NewString()
	labels := map[string]string{"novaforge.io/gate-sandbox": "true", "novaforge.io/org-id": head.OrgID.String(), "novaforge.io/run-id": runID.String()}
	annotations := map[string]string{"novaforge.io/source-sha": head.HeadSHA, "novaforge.io/source-sha256": digest, "novaforge.io/repo-id": head.RepoID.String()}
	namespace, e := s.client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: labels}}, metav1.CreateOptions{})
	if e != nil {
		return nil, 0, e
	}
	var pod *corev1.Pod
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// An exec stream closing is not evidence the process tree stopped. Keep
		// the namespace visible on unknown termination; the pod deadline remains.
		if pod != nil {
			_, _, _ = s.stream(cleanup, ns, []string{"/bin/kill", "-TERM", "1"}, nil)
			e = wait.PollUntilContextTimeout(cleanup, 200*time.Millisecond, 25*time.Second, true, func(ctx context.Context) (bool, error) {
				current, getErr := s.client.CoreV1().Pods(ns).Get(ctx, "analysis", metav1.GetOptions{})
				if getErr != nil {
					return false, getErr
				}
				if current.UID != pod.UID {
					return false, fmt.Errorf("analysis pod identity changed")
				}
				if current.Status.Phase != corev1.PodSucceeded && current.Status.Phase != corev1.PodFailed {
					return false, nil
				}
				if len(current.Status.ContainerStatuses) != 1 || current.Status.ContainerStatuses[0].State.Terminated == nil {
					return false, nil
				}
				return true, nil
			})
			if e != nil {
				err = errors.Join(err, fmt.Errorf("analysis termination unconfirmed in %s: %w", ns, e))
				return
			}
		}
		e = s.client.CoreV1().Namespaces().Delete(cleanup, ns, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &namespace.UID}})
		if e != nil && !apierrors.IsNotFound(e) {
			err = errors.Join(err, fmt.Errorf("analysis cleanup: %w", e))
		}
	}()
	_, err = s.client.NetworkingV1().NetworkPolicies(ns).Create(ctx, &netv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "deny-all", Namespace: ns}, Spec: netv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress, netv1.PolicyTypeEgress}}}, metav1.CreateOptions{})
	if err != nil {
		return
	}
	pod, err = s.client.CoreV1().Pods(ns).Create(ctx, gateSandboxPod(ns, s.image, labels, annotations), metav1.CreateOptions{})
	if err != nil {
		return
	}
	err = wait.PollUntilContextTimeout(ctx, time.Second, 90*time.Second, true, func(ctx context.Context) (bool, error) {
		current, e := s.client.CoreV1().Pods(ns).Get(ctx, "analysis", metav1.GetOptions{})
		if e != nil {
			return false, e
		}
		if current.UID != pod.UID {
			return false, fmt.Errorf("analysis pod identity changed")
		}
		if current.Status.Phase == corev1.PodFailed || current.Status.Phase == corev1.PodSucceeded {
			return false, fmt.Errorf("analysis pod exited before execution")
		}
		return current.Status.Phase == corev1.PodRunning, nil
	})
	if err != nil {
		return
	}
	_, exit, err = s.stream(ctx, ns, []string{"/bin/mkdir", "-p", workspace.Root + "/source"}, nil)
	if err != nil || exit != 0 {
		return nil, 0, fmt.Errorf("prepare analysis source: exit %d: %w", exit, err)
	}
	_, exit, err = s.stream(ctx, ns, []string{"tar", "-xf", "-", "-C", workspace.Root + "/source"}, bytes.NewReader(snapshot))
	if err != nil || exit != 0 {
		return nil, 0, fmt.Errorf("upload analysis source: exit %d: %w", exit, err)
	}
	mapped, report := sandboxArgs(dir, name, args)
	command := append([]string{"/bin/sh", "-c", `cd /workspace/source && exec "$@"`, "analysis", name}, mapped...)
	out, exit, err = s.stream(ctx, ns, command, nil)
	if err != nil {
		return
	}
	if report != "" {
		evidence, code, e := s.stream(ctx, ns, []string{"cat", workspace.Root + "/report"}, nil)
		if e != nil || code != 0 {
			return nil, 0, fmt.Errorf("read analysis evidence: exit %d: %w", code, e)
		}
		// The destination was allocated by analysis in the service, not chosen
		// by the repository. Never unpack tenant-generated archives on the host.
		if e = os.WriteFile(report, evidence, 0600); e != nil {
			return nil, 0, e
		}
	}
	return
}

// Translate only host paths allocated by the trusted analysis functions.
func sandboxArgs(dir, name string, args []string) ([]string, string) {
	mapped := append([]string(nil), args...)
	report := ""
	for i, arg := range mapped {
		if arg == dir {
			mapped[i] = workspace.Root + "/source"
		}
		if name == "go" && strings.HasPrefix(arg, "-coverprofile=") {
			report = strings.TrimPrefix(arg, "-coverprofile=")
			mapped[i] = "-coverprofile=" + workspace.Root + "/report"
		}
		if name == "gitleaks" && i > 0 && args[i-1] == "--report-path" {
			report = arg
			mapped[i] = workspace.Root + "/report"
		}
	}
	return mapped, report
}

type sandboxBuffer struct{ bytes.Buffer }

func (b *sandboxBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > sandboxOutputLimit {
		return 0, fmt.Errorf("analysis output exceeds %d bytes", sandboxOutputLimit)
	}
	return b.Buffer.Write(p)
}
func (s *AnalysisSandbox) stream(ctx context.Context, ns string, command []string, stdin io.Reader) ([]byte, int, error) {
	req := s.client.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name("analysis").SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: "analysis", Command: command, Stdin: stdin != nil, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(s.config, "POST", req.URL())
	if err != nil {
		return nil, 0, err
	}
	var stdout, stderr sandboxBuffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: &stdout, Stderr: &stderr})
	var code utilexec.ExitError
	if errors.As(err, &code) {
		if stdout.Len() == 0 {
			return stderr.Bytes(), code.ExitStatus(), nil
		}
		return stdout.Bytes(), code.ExitStatus(), nil
	}
	if err != nil {
		return nil, 0, err
	}
	return stdout.Bytes(), 0, nil
}

// ControllerOption configures privileged dependencies at service startup only.
type ControllerOption func(*controllerConfig)
type controllerConfig struct {
	sandbox      *AnalysisSandbox
	proofContext func(context.Context) (context.Context, error)
}

func WithAnalysisSandbox(s *AnalysisSandbox) ControllerOption {
	return func(c *controllerConfig) { c.sandbox = s }
}

// WithProofContext signs only the proof RPC, preserving original authorization
// for Git and Work reads. The service entrypoint supplies its scoped token minter.
func WithProofContext(sign func(context.Context) (context.Context, error)) ControllerOption {
	return func(c *controllerConfig) { c.proofContext = sign }
}
