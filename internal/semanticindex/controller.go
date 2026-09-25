package semanticindex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

const producerLabel = "novaforge.io/semantic-producer"

// Controller alone holds Kubernetes authority. Source and tool inputs cross
// stdin, never credentials or host volumes. Every run has a new namespace.
type Controller struct {
	Client kubernetes.Interface
	REST   *rest.Config
	Config ProducerConfig
}
type Produced struct {
	Snapshot    Snapshot
	Manifest    []byte
	Results     []Result
	Definitions *DefinitionResult
}

func (c *Controller) ExecutionDigest() (string, error) { return c.Config.ExecutionDigest() }
func (c *Controller) Produce(ctx context.Context, s Snapshot) (out Produced, err error) {
	if err = authz.RequireOrg(ctx, s.OrgID); err != nil {
		return out, err
	}
	manifest, err := c.Config.ExecutionManifest()
	if err != nil {
		return out, err
	}
	if s.ExecutionDigest != digest(manifest) || s.RootURI != ProducerRoot {
		return out, fmt.Errorf("controller execution context mismatch")
	}
	if _, err = s.Digest(); err != nil {
		return out, err
	}
	if c.Client == nil || c.REST == nil {
		return out, fmt.Errorf("semantic producer Kubernetes controller unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.Config.DeadlineSeconds)*time.Second)
	defer cancel()
	ns, pod, policy := c.objects(s, time.Now())
	created, err := c.Client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		return out, err
	}
	// Publication waits for confirmed pod removal. An uncertain cleanup never
	// turns tool success into graph evidence; retry uses a fresh namespace.
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		e := c.remove(cleanup, created.Name, created.UID)
		if e != nil {
			err = errors.Join(err, fmt.Errorf("semantic producer cleanup unconfirmed: %w", e))
		}
	}()
	if _, err = c.Client.NetworkingV1().NetworkPolicies(ns.Name).Create(ctx, policy, metav1.CreateOptions{}); err != nil {
		return out, err
	}
	if _, err = c.Client.CoreV1().Pods(ns.Name).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return out, err
	}
	for {
		current, e := c.Client.CoreV1().Pods(ns.Name).Get(ctx, pod.Name, metav1.GetOptions{})
		if e != nil {
			return out, e
		}
		if current.Status.Phase == corev1.PodFailed || current.Status.Phase == corev1.PodSucceeded {
			return out, fmt.Errorf("semantic pod terminated before execution")
		}
		if current.Status.Phase == corev1.PodRunning {
			break
		}
		if e = waitProducer(ctx); e != nil {
			return out, e
		}
	}
	payload, err := json.Marshal(s)
	if err != nil {
		return out, err
	}
	exec, err := c.executor(ns.Name, []string{"/app", "semantic-producer"})
	if err != nil {
		return out, err
	}
	stdout := &boundedBuffer{limit: MaxArtifactBytes}
	stderr := &boundedBuffer{limit: 64 << 10}
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: bytes.NewReader(payload), Stdout: stdout, Stderr: stderr})
	if err != nil {
		return out, fmt.Errorf("semantic producer failed: %w: %s", err, stderr.String())
	}
	results, err := ImportArtifacts(ctx, s, stdout.Bytes())
	if err != nil {
		return out, err
	}
	out = Produced{Snapshot: s, Manifest: manifest, Results: results}
	queries, err := GoDefinitionQueries(s)
	if err != nil {
		return out, err
	}
	if len(queries) > 0 {
		argv := []string{"/app", "semantic-lsp"}
		remote, err := c.executor(ns.Name, argv)
		if err != nil {
			return out, err
		}
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		done := make(chan error, 1)
		go func() {
			e := remote.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: inR, Stdout: outW, Stderr: io.Discard})
			_ = outW.CloseWithError(e)
			_ = inR.CloseWithError(e)
			done <- e
		}()
		d, _ := s.Digest()
		defs, e := ResolveDefinitions(ctx, &producerTransport{outR, inW}, s, RunEvidence{Revision: s.Revision, SnapshotDigest: d, ExecutionDigest: s.ExecutionDigest, Tool: "gopls", ToolVersion: "v0.21.1"}, queries)
		if e != nil {
			cancel()
			return out, e
		}
		select {
		case e = <-done:
			if e != nil {
				return out, e
			}
		case <-ctx.Done():
			return out, ctx.Err()
		}
		out.Definitions = &defs
	}
	return out, ctx.Err()
}

type producerTransport struct {
	io.ReadCloser
	io.WriteCloser
}

func (p *producerTransport) Close() error {
	e := p.ReadCloser.Close()
	_ = p.WriteCloser.Close()
	return e
}
func waitProducer(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(250 * time.Millisecond):
		return nil
	}
}

func (c *Controller) objects(s Snapshot, now time.Time) (*corev1.Namespace, *corev1.Pod, *networkingv1.NetworkPolicy) {
	name := "nf-semantic-" + uuid.NewString()
	labels := map[string]string{producerLabel: "true", "novaforge.io/org": s.OrgID.String(), "novaforge.io/repo": s.RepoID.String(), "pod-security.kubernetes.io/enforce": "restricted"}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, Annotations: map[string]string{"novaforge.io/expires-at": now.Add(time.Duration(c.Config.DeadlineSeconds+60) * time.Second).UTC().Format(time.RFC3339)}}}
	no, yes := false, true
	uid := int64(65532)
	zero := int64(0)
	resources := corev1.ResourceList{corev1.ResourceCPU: *resource.NewMilliQuantity(c.Config.CPUMilli, resource.DecimalSI), corev1.ResourceMemory: *resource.NewQuantity(c.Config.MemoryMiB<<20, resource.BinarySI), corev1.ResourceEphemeralStorage: *resource.NewQuantity(c.Config.StorageMiB<<20, resource.BinarySI)}
	size := resources[corev1.ResourceEphemeralStorage]
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "producer", Namespace: name, Labels: map[string]string{producerLabel: "true"}}, Spec: corev1.PodSpec{
		AutomountServiceAccountToken: &no, EnableServiceLinks: &no, RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: &c.Config.DeadlineSeconds, TerminationGracePeriodSeconds: &zero,
		NodeSelector:    map[string]string{"kubernetes.io/arch": c.Config.Architecture, "kubernetes.io/os": "linux"},
		SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &yes, RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Containers:      []corev1.Container{{Name: "producer", Image: c.Config.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/bin/sleep", "600"}, WorkingDir: "/work/source", Resources: corev1.ResourceRequirements{Requests: resources, Limits: resources}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, VolumeMounts: []corev1.VolumeMount{{Name: "work", MountPath: "/work"}}}},
		Volumes:         []corev1.Volume{{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &size}}}},
	}}
	// WorkingDir must exist before exec; the holding process starts at the mount root.
	pod.Spec.Containers[0].WorkingDir = "/work"
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "deny-all", Namespace: name}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}}}
	return ns, pod, policy
}

func (c *Controller) executor(ns string, argv []string) (remotecommand.Executor, error) {
	req := c.Client.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name("producer").SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: "producer", Command: argv, Stdin: true, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	ws, e := remotecommand.NewWebSocketExecutor(c.REST, "GET", req.URL().String())
	if e != nil {
		return nil, e
	}
	spdy, e := remotecommand.NewSPDYExecutor(c.REST, "POST", req.URL())
	if e != nil {
		return nil, e
	}
	return remotecommand.NewFallbackExecutor(ws, spdy, func(e error) bool { return httpstream.IsUpgradeFailure(e) || httpstream.IsHTTPSProxyError(e) })
}
func (c *Controller) remove(ctx context.Context, name string, uid types.UID) error {
	if !strings.HasPrefix(name, "nf-semantic-") || uid == "" {
		return fmt.Errorf("invalid owned namespace")
	}
	err := c.Client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	for {
		_, err = c.Client.CoreV1().Pods(name).Get(ctx, "producer", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = waitProducer(ctx); err != nil {
			return err
		}
	}
}

// Reap recovers only expired namespaces created by this controller, with UID
// preconditions. Pod activeDeadlineSeconds remains a hard bound after a crash.
func (c *Controller) Reap(ctx context.Context) error {
	namespaces, err := c.Client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: producerLabel + "=true"})
	if err != nil {
		return err
	}
	var errs []error
	for _, ns := range namespaces.Items {
		expiry, e := time.Parse(time.RFC3339, ns.Annotations["novaforge.io/expires-at"])
		if e != nil || time.Now().Before(expiry) {
			continue
		}
		if e = c.remove(ctx, ns.Name, ns.UID); e != nil {
			errs = append(errs, e)
		}
	}
	return errors.Join(errs...)
}
