package workspace

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
)

var ErrWorkspaceClosed = errors.New("workspace invalidated by stdio closure")

// Identity is trusted lifecycle evidence, recorded BEFORE a stdio process is
// launched. UIDs prevent teardown from deleting replacement resources. Observed
// termination is persisted before namespace deletion so restart can retry safely.
type Identity struct {
	OrgID               uuid.UUID
	RunID               uuid.UUID
	NamespaceUID        types.UID
	PodUID              types.UID
	NodeName            string
	TerminationObserved bool
}

// WithCleanupRecorder binds durable agents-owned cleanup intent. It must be
// configured once, before serving; no stdio command starts without its receipt.
func (p *Provisioner) WithCleanupRecorder(record func(context.Context, Identity) error) *Provisioner {
	p.recordCleanup = record
	return p
}

func (p *Provisioner) stdioIdentity(ctx context.Context, runID uuid.UUID) (Identity, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return Identity{}, fmt.Errorf("stdio requires organization scope")
	}
	ns, err := p.client.CoreV1().Namespaces().Get(ctx, namespaceFor(runID), metav1.GetOptions{})
	if err != nil {
		return Identity{}, err
	}
	pod, err := p.client.CoreV1().Pods(ns.Name).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return Identity{}, err
	}
	if ns.Labels[runIDLabel] != runID.String() || ns.Labels[orgIDLabel] != scope.OrgID.String() || pod.Labels[runIDLabel] != runID.String() || pod.UID == "" || ns.UID == "" {
		return Identity{}, fmt.Errorf("workspace identity mismatch")
	}
	return Identity{OrgID: scope.OrgID, RunID: runID, NamespaceUID: ns.UID, PodUID: pod.UID, NodeName: pod.Spec.NodeName}, nil
}

// DestroyConfirmed requires observed container termination, not just API
// disappearance. Node partitions and a missed termination event remain unknown
// and pending; stream closure or an absent pod never invents process-death proof.
func (p *Provisioner) DestroyConfirmed(ctx context.Context, id Identity) error {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID != id.OrgID || id.OrgID == uuid.Nil || id.NamespaceUID == "" || id.PodUID == "" {
		return fmt.Errorf("workspace cleanup identity unavailable")
	}
	p.invalidated.Store(id.RunID, true)
	semaphore := make(chan struct{}, 1)
	semaphore <- struct{}{}
	lock, _ := p.cleanupLocks.LoadOrStore(id.RunID, semaphore)
	gate := lock.(chan struct{})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gate:
	}
	defer func() { gate <- struct{}{} }()
	if known, ok := p.terminationEvidence.Load(id.RunID); ok {
		saved := known.(Identity)
		if saved.NamespaceUID == id.NamespaceUID && saved.PodUID == id.PodUID && saved.OrgID == id.OrgID {
			id.TerminationObserved = true
		}
	}

	nsName := namespaceFor(id.RunID)
	ns, err := p.client.CoreV1().Namespaces().Get(ctx, nsName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && id.TerminationObserved {
		return nil
	}
	if err != nil {
		return err
	}
	if ns.UID != id.NamespaceUID || ns.Labels[orgIDLabel] != id.OrgID.String() || ns.Labels[runIDLabel] != id.RunID.String() {
		return fmt.Errorf("replacement workspace namespace refused")
	}
	pod, err := p.client.CoreV1().Pods(nsName).Get(ctx, podName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && pod.UID != id.PodUID {
		return fmt.Errorf("replacement workspace pod refused")
	}
	if !id.TerminationObserved {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("pod absent without recorded termination evidence")
		}
		if pod.Spec.NodeName != id.NodeName {
			return fmt.Errorf("workspace node identity changed")
		}
		if !podTerminated(pod) {
			events, err := p.client.CoreV1().Pods(nsName).Watch(ctx, metav1.ListOptions{FieldSelector: "metadata.name=" + podName, ResourceVersion: pod.ResourceVersion})
			if err != nil {
				return err
			}
			defer events.Stop()
			if err = p.client.CoreV1().Pods(nsName).Delete(ctx, podName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &id.PodUID}}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			stopped := false
			for !stopped {
				select {
				case <-ctx.Done():
					return fmt.Errorf("workspace termination unconfirmed: %w", ctx.Err())
				case event, ok := <-events.ResultChan():
					if !ok {
						return fmt.Errorf("workspace termination watch ended without evidence")
					}
					observed, ok := event.Object.(*corev1.Pod)
					if !ok {
						if event.Type == watch.Error {
							return fmt.Errorf("workspace termination watch failed")
						}
						continue
					}
					if observed.UID != id.PodUID {
						return fmt.Errorf("replacement pod during termination")
					}
					stopped = podTerminated(observed)
					if event.Type == watch.Deleted && !stopped {
						return fmt.Errorf("workspace disappeared without termination evidence")
					}
				}
			}
		}
		id.TerminationObserved = true
		if p.recordCleanup == nil {
			return fmt.Errorf("workspace termination recorder unavailable")
		}
		if err = p.recordCleanup(ctx, id); err != nil {
			return err
		}
		p.terminationEvidence.Store(id.RunID, id)
	}
	if err = p.client.CoreV1().Namespaces().Delete(ctx, nsName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &id.NamespaceUID}}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		ns, err = p.client.CoreV1().Namespaces().Get(ctx, nsName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if ns.UID != id.NamespaceUID {
			return fmt.Errorf("replacement namespace while waiting for cleanup")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func podTerminated(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		return false
	}
	if len(pod.Status.ContainerStatuses) != len(pod.Spec.Containers) {
		return false
	}
	for _, s := range pod.Status.ContainerStatuses {
		if s.State.Terminated == nil {
			return false
		}
	}
	return true
}
