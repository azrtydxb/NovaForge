// Package workspace provisions per-agent-run Kubernetes namespaces: a
// namespace, a default-deny NetworkPolicy, a ResourceQuota, and a pod
// mounting the repository volume read-only. Every workspace is destroyed
// when the run ends, and Reap deletes anything a crashed controller left
// behind.
package workspace

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// runIDLabel labels every namespace and pod this package creates with the
// agent run it belongs to.
const runIDLabel = "novaforge.io/run-id"

// createdAtLabel records a namespace's creation time as a label (rather than
// relying solely on the Kubernetes object's CreationTimestamp) so Reap can
// filter on it directly, including against the fake clientset used in tests.
//
// The value is Unix seconds, not RFC 3339. A label value may contain only
// alphanumerics, '-', '_' and '.', and RFC 3339 has colons in it: the API
// server rejected every namespace this package tried to create, so no agent
// workspace could ever be provisioned. The fake clientset does not validate
// label syntax, which is exactly why the unit tests were happy.
const createdAtLabel = "novaforge.io/created-at"

// Spec describes the workspace to provision for one agent run.
type Spec struct {
	Image    string
	Env      map[string]string
	CPULimit string
	MemLimit string
	RepoPVC  string
}

// Workspace identifies the namespace and pod provisioned for one run.
type Workspace struct {
	Namespace string
	PodName   string
}

// Provisioner creates and tears down isolated per-run Kubernetes workspaces.
type Provisioner struct {
	client     kubernetes.Interface
	restConfig *rest.Config
}

// podName and containerName name the single pod and container of every
// workspace; Exec addresses them.
const (
	podName       = "agent"
	containerName = "agent"
)

var metav1GetOptions = metav1.GetOptions{}

// NewProvisioner wraps client as a Provisioner. client is typically a real
// cluster clientset in production and k8s.io/client-go/kubernetes/fake in
// tests.
func NewProvisioner(client kubernetes.Interface) *Provisioner {
	return &Provisioner{client: client}
}

// namespaceFor returns the deterministic namespace name for runID.
func namespaceFor(runID uuid.UUID) string {
	return "nf-run-" + runID.String()
}

// Create provisions a namespace for runID, applies a default-deny
// NetworkPolicy and a ResourceQuota derived from spec, then creates a pod
// running spec.Image that mounts the repository PVC read-only.
func (p *Provisioner) Create(ctx context.Context, runID uuid.UUID, spec Spec) (Workspace, error) {
	ns := namespaceFor(runID)

	_, err := p.client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
			Labels: map[string]string{
				runIDLabel:     runID.String(),
				createdAtLabel: strconv.FormatInt(time.Now().UTC().Unix(), 10),
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return Workspace{}, fmt.Errorf("create namespace %s: %w", ns, err)
	}

	if err := p.applyDenyAllNetworkPolicy(ctx, ns); err != nil {
		return Workspace{}, err
	}

	if err := p.applyResourceQuota(ctx, ns, spec); err != nil {
		return Workspace{}, err
	}

	podName, err := p.createPod(ctx, ns, runID, spec)
	if err != nil {
		return Workspace{}, err
	}

	return Workspace{Namespace: ns, PodName: podName}, nil
}

func (p *Provisioner) applyDenyAllNetworkPolicy(ctx context.Context, ns string) error {
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default-deny-all",
			Namespace: ns,
		},
		Spec: networkingv1.NetworkPolicySpec{
			// An empty pod selector matches every pod in the namespace, and
			// declaring the Ingress policy type with no rules denies all
			// ingress traffic by default.
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}
	if _, err := p.client.NetworkingV1().NetworkPolicies(ns).Create(ctx, np, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("apply default-deny network policy in %s: %w", ns, err)
	}
	return nil
}

func (p *Provisioner) applyResourceQuota(ctx context.Context, ns string, spec Spec) error {
	hard := corev1.ResourceList{}
	if spec.CPULimit != "" {
		hard[corev1.ResourceLimitsCPU] = resource.MustParse(spec.CPULimit)
	}
	if spec.MemLimit != "" {
		hard[corev1.ResourceLimitsMemory] = resource.MustParse(spec.MemLimit)
	}
	hard[corev1.ResourcePods] = resource.MustParse("1")

	rq := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-quota",
			Namespace: ns,
		},
		Spec: corev1.ResourceQuotaSpec{Hard: hard},
	}
	if _, err := p.client.CoreV1().ResourceQuotas(ns).Create(ctx, rq, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("apply resource quota in %s: %w", ns, err)
	}
	return nil
}

func (p *Provisioner) createPod(ctx context.Context, ns string, runID uuid.UUID, spec Spec) (string, error) {
	var env []corev1.EnvVar
	for k, v := range spec.Env {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}

	resources := corev1.ResourceRequirements{Limits: corev1.ResourceList{}}
	if spec.CPULimit != "" {
		resources.Limits[corev1.ResourceCPU] = resource.MustParse(spec.CPULimit)
	}
	if spec.MemLimit != "" {
		resources.Limits[corev1.ResourceMemory] = resource.MustParse(spec.MemLimit)
	}

	// A workspace always has writable scratch space of its own; it mounts a
	// repository checkout only when the caller supplied a claim for one.
	// Declaring the repo volume unconditionally produced a pod with an empty
	// persistentVolumeClaim.claimName, which the API server refuses outright
	// — so no agent pod could ever start. The fake clientset accepts it,
	// which is why every unit test passed.
	const (
		repoVolume  = "repo"
		scratchVol  = "workspace"
		workspaceAt = Root
	)

	mounts := []corev1.VolumeMount{
		{Name: scratchVol, MountPath: workspaceAt},
	}
	volumes := []corev1.Volume{
		{
			Name:         scratchVol,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
	}
	if spec.RepoPVC != "" {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      repoVolume,
			MountPath: workspaceAt + "/repo",
			ReadOnly:  true,
		})
		volumes = append(volumes, corev1.Volume{
			Name: repoVolume,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: spec.RepoPVC,
					ReadOnly:  true,
				},
			},
		})
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
			Labels: map[string]string{
				runIDLabel: runID.String(),
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:  containerName,
					Image: spec.Image,
					// The pod is the run's workspace: the agent's files are
					// written into it and its commands run in it, over exec.
					// With the image's default command (a shell with no
					// terminal) it exited at once, so nothing could ever act
					// inside it and files were staged in agent-runtime's own
					// filesystem instead.
					Command:      []string{"sleep", "infinity"},
					WorkingDir:   workspaceAt,
					Env:          env,
					Resources:    resources,
					VolumeMounts: mounts,
				},
			},
			Volumes: volumes,
		},
	}

	if _, err := p.client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("create pod in %s: %w", ns, err)
	}
	return podName, nil
}

// Destroy deletes the namespace provisioned for runID, tearing down every
// object inside it.
func (p *Provisioner) Destroy(ctx context.Context, runID uuid.UUID) error {
	ns := namespaceFor(runID)
	if err := p.client.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete namespace %s: %w", ns, err)
	}
	return nil
}

// Reap deletes every run namespace whose novaforge.io/created-at label is
// older than olderThan and returns how many were deleted. It exists so a
// crashed or restarted controller can never leak workspaces indefinitely:
// running it on a ticker is the sole cleanup mechanism required.
func (p *Provisioner) Reap(ctx context.Context, olderThan time.Duration) (int, error) {
	list, err := p.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: runIDLabel,
	})
	if err != nil {
		return 0, fmt.Errorf("list run namespaces: %w", err)
	}

	cutoff := time.Now().Add(-olderThan)
	reaped := 0
	for _, ns := range list.Items {
		createdRaw, ok := ns.Labels[createdAtLabel]
		if !ok {
			continue
		}
		secs, err := strconv.ParseInt(createdRaw, 10, 64)
		if err != nil {
			continue
		}
		created := time.Unix(secs, 0).UTC()
		if created.After(cutoff) {
			continue
		}
		if err := p.client.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return reaped, fmt.Errorf("reap namespace %s: %w", ns.Name, err)
		}
		reaped++
	}
	return reaped, nil
}
