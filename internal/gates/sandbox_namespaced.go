package gates

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/scheme"
	typedcore "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

// NamespacedSandboxConfig is trusted startup configuration, never repository or
// request input. Provisioning, RBAC and image qualification are separate gates.
// The journal supplies the immutable target/namespace and capacity ceiling.
// Recovery must retain this profile AND the original target's client mapping.
type NamespacedSandboxConfig struct {
	Image                string
	ApplicationNamespace string
	ServiceAccount       string
}

// SandboxPodReaderDeleter deliberately cannot create, exec, list, mutate policies
// or manage namespaces. Supply a client authenticated as the separate observer,
// bound to the journal namespace. No tenant credential or context is retained.
type SandboxPodReaderDeleter interface {
	Get(context.Context, string, metav1.GetOptions) (*corev1.Pod, error)
	Delete(context.Context, string, metav1.DeleteOptions) error
}

var ErrSandboxPending = errors.New("sandbox termination or cleanup remains unconfirmed")

// NamespacedSandbox manages pod lifecycle only. It is not an analysis.Exec and
// does not replace the old allocator until the parent explicitly wires it.
type NamespacedSandbox struct {
	journal *SandboxJournal
	pods    typedcore.PodInterface
	create  rest.Interface
	profile NamespacedSandboxConfig
}

func validateNamespacedSandbox(j *SandboxJournal, c NamespacedSandboxConfig) error {
	if j == nil || j.pool == nil || strings.TrimSpace(j.target) == "" || !immutableAnalysisImage.MatchString(c.Image) ||
		len(validation.IsDNS1123Label(j.namespace)) != 0 || j.namespace == "default" || strings.HasPrefix(j.namespace, "kube-") ||
		len(validation.IsDNS1123Label(c.ApplicationNamespace)) != 0 || j.namespace == c.ApplicationNamespace ||
		len(validation.IsDNS1123Subdomain(c.ServiceAccount)) != 0 || c.ServiceAccount == "default" {
		return fmt.Errorf("invalid namespaced sandbox startup profile: %w", ErrSandboxConflict)
	}
	return nil
}

// NewNamespacedSandbox owns Create transport: ordinary PodInterface.Create may
// retry POST on Retry-After. MaxRetries(0) below disables that client-go retry.
// Config must select the original journal target and use trusted non-replaying
// transport/authentication wrappers and proxies; arbitrary custom RoundTrippers
// can duplicate requests outside this component's guarantee.
func NewNamespacedSandbox(j *SandboxJournal, config *rest.Config, c NamespacedSandboxConfig) (*NamespacedSandbox, error) {
	if err := validateNamespacedSandbox(j, c); err != nil {
		return nil, err
	}
	if config == nil {
		return nil, fmt.Errorf("sandbox Kubernetes configuration required")
	}
	config = rest.CopyConfig(config)
	httpClient, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, err
	}
	// HTTPClientFor may return http.DefaultClient. Never mutate that shared
	// client; redirects (especially 307/308) otherwise replay the POST body.
	isolatedClient := *httpClient
	isolatedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client, err := typedcore.NewForConfigAndClient(config, &isolatedClient)
	if err != nil {
		return nil, err
	}
	return &NamespacedSandbox{journal: j, pods: client.Pods(j.namespace), create: client.RESTClient(), profile: c}, nil
}

// Allocate reserves durable capacity, acknowledges exactly one create claim,
// creates once, then acknowledges UID binding. On ANY error UID is empty and
// identity is reconciliation-only, never usable execution/redispatch authority.
// Reserve acknowledgement loss may be retried with the exact intent, but a
// persisted Create claim is never redeemed again (including AlreadyExists).
func (s *NamespacedSandbox) Allocate(ctx context.Context, intent SandboxIntent) (SandboxIdentity, string, error) {
	// Copy the only reference-valued identity field before retaining/encoding it.
	intent.Containers = append([]string(nil), intent.Containers...)
	if err := s.profile.validateIntent(intent); err != nil {
		return SandboxIdentity{}, "", err
	}
	v, err := s.journal.Reserve(ctx, intent)
	if err != nil {
		return SandboxIdentity{}, "", err
	}
	id := v.Identity
	if _, err = s.journal.ClaimCreate(ctx, id); err != nil {
		return id, "", err
	}
	p := new(corev1.Pod)
	err = s.create.Post().Namespace(id.Namespace).Resource("pods").VersionedParams(&metav1.CreateOptions{}, scheme.ParameterCodec).Body(s.profile.pod(id)).MaxRetries(0).Do(ctx).Into(p)
	if err != nil {
		return id, "", err
	}
	if err = s.profile.validatePod(id, "", p); err != nil {
		return id, "", err
	}
	if err = s.journal.BindPodUID(ctx, id, string(p.UID)); err != nil {
		return id, "", err
	}
	return id, string(p.UID), nil
}

// ValidateForExec performs a fresh exact GET, intended before EACH future mkdir,
// upload, command, report read and termination exec. The parent still owns the
// original acknowledged ClaimExec and output provenance; readback of that claim
// is NOT dispatch authority. This method cannot atomically bind a later exec to
// the UID: Kubernetes exec has no UID precondition. Safety requires an exclusive
// trusted allocator that never reuses names; tenants/janitors cannot create pods.
// An operator with equivalent creation authority is outside that guarantee.
func (s *NamespacedSandbox) ValidateForExec(ctx context.Context, id SandboxIdentity, uid string) (*corev1.Pod, error) {
	v, err := s.journal.ReadExact(ctx, id)
	if err != nil {
		return nil, err
	}
	if !sandboxUID(&v, uid) || v.ExecClaim == nil || v.Terminal != nil || v.Cleanup != nil || v.Released {
		return nil, ErrSandboxTransition
	}
	p, err := s.pods.Get(ctx, id.PodName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if err = s.profile.validatePod(id, uid, p); err != nil {
		return nil, err
	}
	if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
		return nil, ErrSandboxPending
	}
	return p, nil
}
