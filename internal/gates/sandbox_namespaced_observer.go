package gates

import (
	"context"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// NamespacedSandboxObserver has no Create/exec capability and stores no tenant
// credential/context. The parent supplies separately authenticated GET/DELETE
// credentials, re-enters org scope, and enumerates durable identities elsewhere.
type NamespacedSandboxObserver struct {
	journal *SandboxJournal
	pods    SandboxPodReaderDeleter
	profile NamespacedSandboxConfig
}

func NewNamespacedSandboxObserver(j *SandboxJournal, pods SandboxPodReaderDeleter, c NamespacedSandboxConfig) (*NamespacedSandboxObserver, error) {
	if err := validateNamespacedSandbox(j, c); err != nil {
		return nil, err
	}
	if pods == nil {
		return nil, ErrSandboxConflict
	}
	return &NamespacedSandboxObserver{journal: j, pods: pods, profile: c}, nil
}

// Observe attempts one settlement pass on the exact journal name. Nil means
// lifecycle settlement, NEVER a successful tool/gate result. Uncertainty retains
// capacity and the original name. Retrying observation cannot create or exec.
func (s *NamespacedSandboxObserver) Observe(ctx context.Context, id SandboxIdentity) error {
	v, err := s.journal.ReadExact(ctx, id)
	if err != nil {
		return err
	}
	if v.Released {
		return nil
	}
	if v.CreateClaim == nil {
		return ErrSandboxPending
	}
	p, err := s.pods.Get(ctx, id.PodName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return s.recordAbsence(ctx, v)
	}
	if err != nil {
		return err
	}
	uid := ""
	if v.PodUID != nil {
		uid = *v.PodUID
	}
	if err = s.profile.validatePod(id, uid, p); err != nil {
		return err
	}
	if v.PodUID == nil {
		// Validated readback may recover cleanup identity, never dispatch authority.
		uid = string(p.UID)
		if err = s.journal.BindPodUID(ctx, id, uid); err != nil {
			return err
		}
		v.PodUID = &uid
	}
	receipt, err := namespacedTerminalReceipt(p)
	if err != nil {
		return err
	}
	if v.Terminal != nil {
		// Reuse the first durable observation timestamp; conflicting container/phase
		// evidence still fails the journal's exact immutable receipt comparison.
		receipt.ObservedAt = v.Terminal.ObservedAt
	}
	if err = s.journal.RecordTerminal(ctx, id, receipt); err != nil {
		return err
	}
	if v.Cleanup == nil {
		podUID := types.UID(uid)
		if err = s.pods.Delete(ctx, id.PodName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &podUID}}); err != nil {
			// NotFound/timeout are NOT acknowledged UID-bound deletion receipts.
			return err
		}
		cleanup := SandboxCleanupReceipt{PodUID: uid, DeleteID: uuid.New(), AcknowledgedAt: time.Now().UTC()}
		if err = s.journal.RecordCleanup(ctx, id, cleanup); err != nil {
			return err
		}
	}
	// Read exact durable receipts rather than inventing a new DeleteID or clock
	// value after restart/concurrent observation or a lost database acknowledgement.
	v, err = s.journal.ReadExact(ctx, id)
	if err != nil {
		return err
	}
	p, err = s.pods.Get(ctx, id.PodName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return s.recordAbsence(ctx, v)
	}
	if err != nil {
		return err
	}
	if err = s.profile.validatePod(id, uid, p); err != nil {
		return err
	}
	return ErrSandboxPending
}

func (s *NamespacedSandboxObserver) recordAbsence(ctx context.Context, v SandboxInvocation) error {
	if v.Terminal == nil || v.Cleanup == nil || v.PodUID == nil {
		return ErrSandboxPending
	}
	if v.Absence != nil {
		return nil
	}
	return s.journal.RecordAbsence(ctx, v.Identity, SandboxAbsenceReceipt{PodUID: *v.PodUID, DeleteID: v.Cleanup.DeleteID, ObservedAt: time.Now().UTC()})
}

func namespacedTerminalReceipt(p *corev1.Pod) (SandboxTerminalReceipt, error) {
	if (p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed) || len(p.Status.ContainerStatuses) != 1 {
		return SandboxTerminalReceipt{}, ErrSandboxPending
	}
	status := p.Status.ContainerStatuses[0]
	terminal := status.State.Terminated
	now := time.Now().UTC()
	if terminal == nil || status.State.Running != nil || status.State.Waiting != nil || terminal.FinishedAt.IsZero() || terminal.FinishedAt.After(now) {
		return SandboxTerminalReceipt{}, ErrSandboxPending
	}
	return SandboxTerminalReceipt{PodUID: string(p.UID), Phase: string(p.Status.Phase), ObservedAt: now, Containers: []SandboxContainerTerminal{{Name: status.Name, ExitCode: terminal.ExitCode, FinishedAt: terminal.FinishedAt.Time}}}, nil
}
