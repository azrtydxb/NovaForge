package gates

import (
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
)

const namespacedIdentityAnnotation = "novaforge.io/sandbox-identity"

func (c NamespacedSandboxConfig) validateIntent(i SandboxIntent) error {
	if err := i.validate(); err != nil {
		return err
	}
	if i.ImageDigest != strings.Split(c.Image, "@")[1] || i.Container != "analysis" || len(i.Containers) != 1 || i.Containers[0] != "analysis" {
		return ErrSandboxConflict
	}
	return nil
}

func (c NamespacedSandboxConfig) pod(id SandboxIdentity) *corev1.Pod {
	// Every immutable field, including long digests, belongs in annotations, not
	// labels (Kubernetes label values are limited to 63 characters).
	identity, _ := json.Marshal(id) // concrete strings/UUIDs/slices cannot fail to encode
	p := gateSandboxPod(id.Namespace, c.Image, map[string]string{"novaforge.io/gate-sandbox": "true"}, map[string]string{namespacedIdentityAnnotation: string(identity), "novaforge.io/sandbox-profile": "namespaced-v1"})
	p.Name = id.PodName
	p.Spec.ServiceAccountName = c.ServiceAccount
	p.Spec.DeprecatedServiceAccount = c.ServiceAccount
	p.Spec.DNSPolicy = corev1.DNSClusterFirst
	p.Spec.SchedulerName = corev1.DefaultSchedulerName
	p.Spec.Priority = new(int32(0))
	p.Spec.PreemptionPolicy = new(corev1.PreemptLowerPriority)
	// Kubernetes defaults a missing request to its limit. Set it explicitly so
	// the persisted profile does not depend on API-side resource defaulting.
	p.Spec.Containers[0].Resources.Requests[corev1.ResourceEphemeralStorage] = p.Spec.Containers[0].Resources.Limits[corev1.ResourceEphemeralStorage]
	p.Spec.Containers[0].TerminationMessagePath = corev1.TerminationMessagePathDefault
	p.Spec.Containers[0].TerminationMessagePolicy = corev1.TerminationMessageReadFile
	// Explicit standard DefaultTolerationSeconds admission values. No other
	// injected toleration, priority, scheduling or security profile is accepted.
	p.Spec.Tolerations = []corev1.Toleration{
		{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: new(int64(300))},
		{Key: "node.kubernetes.io/unreachable", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: new(int64(300))},
	}
	return p
}

func (c NamespacedSandboxConfig) validatePod(id SandboxIdentity, uid string, p *corev1.Pod) error {
	if err := c.validateIntent(id.SandboxIntent); err != nil {
		return err
	}
	if p == nil || p.Name != id.PodName || p.Namespace != id.Namespace || strings.TrimSpace(string(p.UID)) == "" || (uid != "" && string(p.UID) != uid) {
		return ErrSandboxConflict
	}
	expected := c.pod(id)
	if !apiequality.Semantic.DeepEqual(p.Labels, expected.Labels) || !apiequality.Semantic.DeepEqual(p.Annotations, expected.Annotations) || len(p.OwnerReferences) != 0 || p.GenerateName != "" {
		return ErrSandboxConflict
	}
	actual := p.Spec.DeepCopy()
	// NodeName is the scheduler's binding, not executable identity or proof of
	// termination. All other PodSpec fields are compared, including future API
	// fields, init/ephemeral containers, credential projections and image pulls.
	actual.NodeName = ""
	if !apiequality.Semantic.DeepEqual(actual, &expected.Spec) {
		return fmt.Errorf("sandbox pod profile changed: %w", ErrSandboxConflict)
	}
	// Empty statuses are legitimate before scheduling. If supplied, never allow
	// a foreign container status to become execution or terminal evidence.
	if len(p.Status.InitContainerStatuses) != 0 || len(p.Status.EphemeralContainerStatuses) != 0 || len(p.Status.ContainerStatuses) > 1 {
		return ErrSandboxConflict
	}
	for _, status := range p.Status.ContainerStatuses {
		if status.Name != id.Container || (status.Image != "" && status.Image != c.Image) {
			return ErrSandboxConflict
		}
	}
	return nil
}
