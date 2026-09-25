package gates

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

// ClaimCreate returns authority ONLY on the first acknowledged commit. A claim
// means Create may have been sent, not that Kubernetes acknowledged it. Reading
// that claim or retrying this method after any error never authorizes replay.
func (s *SandboxJournal) ClaimCreate(ctx context.Context, identity SandboxIdentity) (uuid.UUID, error) {
	claim := uuid.New()
	err := s.change(ctx, identity, true, func(v *SandboxInvocation) error {
		if v.CreateClaim != nil {
			return ErrSandboxClaimed
		}
		v.CreateClaim = &claim
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return claim, nil
}

// BindPodUID must be acknowledged before any exec, including source preparation.
// Recovery may bind an observed UID only after validating the complete identity.
func (s *SandboxJournal) BindPodUID(ctx context.Context, identity SandboxIdentity, uid string) error {
	return s.change(ctx, identity, false, func(v *SandboxInvocation) error {
		if strings.TrimSpace(uid) == "" || v.CreateClaim == nil {
			return ErrSandboxTransition
		}
		if v.PodUID != nil {
			if *v.PodUID != uid {
				return ErrSandboxConflict
			}
			return nil
		}
		v.PodUID = &uid
		return nil
	})
}

// ClaimExec is the one-way admission for the invocation's executable sequence,
// not a reusable permission token. Only its acknowledged caller may proceed.
// Parent executor must GET/validate identity before EACH exec and never recover
// by replaying this sequence; Kubernetes exec itself has no UID precondition.
func (s *SandboxJournal) ClaimExec(ctx context.Context, identity SandboxIdentity, uid string) (uuid.UUID, error) {
	claim := uuid.New()
	err := s.change(ctx, identity, true, func(v *SandboxInvocation) error {
		if v.ExecClaim != nil {
			return ErrSandboxClaimed
		}
		if !sandboxUID(v, uid) || v.Terminal != nil || v.Cleanup != nil || v.Released {
			return ErrSandboxTransition
		}
		v.ExecClaim = &claim
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return claim, nil
}

func sandboxUID(v *SandboxInvocation, uid string) bool {
	return v.PodUID != nil && uid != "" && *v.PodUID == uid
}

func sameSandboxReceipt(a, b any) bool {
	x, e1 := json.Marshal(a)
	y, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}

func (s *SandboxJournal) RecordTerminal(ctx context.Context, identity SandboxIdentity, r SandboxTerminalReceipt) error {
	return s.change(ctx, identity, false, func(v *SandboxInvocation) error {
		if v.Terminal != nil {
			if !sameSandboxReceipt(v.Terminal, r) {
				return ErrSandboxConflict
			}
			return nil
		}
		if !sandboxUID(v, r.PodUID) || (r.Phase != "Succeeded" && r.Phase != "Failed") || r.ObservedAt.IsZero() || len(r.Containers) != len(identity.Containers) {
			return ErrSandboxTransition
		}
		for n, c := range r.Containers {
			if c.Name != identity.Containers[n] || c.FinishedAt.IsZero() || c.FinishedAt.After(r.ObservedAt) || (r.Phase == "Succeeded" && c.ExitCode != 0) {
				return ErrSandboxTransition
			}
		}
		v.Terminal = &r
		return nil
	})
}

func (s *SandboxJournal) RecordCleanup(ctx context.Context, identity SandboxIdentity, r SandboxCleanupReceipt) error {
	return s.change(ctx, identity, false, func(v *SandboxInvocation) error {
		if v.Cleanup != nil {
			if !sameSandboxReceipt(v.Cleanup, r) {
				return ErrSandboxConflict
			}
			return nil
		}
		if !sandboxUID(v, r.PodUID) || v.Terminal == nil || r.DeleteID == uuid.Nil || r.AcknowledgedAt.IsZero() || r.AcknowledgedAt.Before(v.Terminal.ObservedAt) {
			return ErrSandboxTransition
		}
		v.Cleanup = &r
		return nil
	})
}

// RecordAbsence is the ONLY release path. Unknown/expired/missing objects before
// terminal evidence and acknowledged UID-bound cleanup cannot produce release.
func (s *SandboxJournal) RecordAbsence(ctx context.Context, identity SandboxIdentity, r SandboxAbsenceReceipt) error {
	return s.change(ctx, identity, false, func(v *SandboxInvocation) error {
		if v.Absence != nil {
			if !sameSandboxReceipt(v.Absence, r) {
				return ErrSandboxConflict
			}
			return nil
		}
		if !sandboxUID(v, r.PodUID) || v.Terminal == nil || v.Cleanup == nil || r.DeleteID != v.Cleanup.DeleteID || r.ObservedAt.IsZero() || r.ObservedAt.Before(v.Cleanup.AcknowledgedAt) {
			return ErrSandboxTransition
		}
		v.Absence = &r
		v.Released = true
		return nil
	})
}

func (s *SandboxJournal) RecordToolResult(ctx context.Context, identity SandboxIdentity, r SandboxToolResult) error {
	return s.change(ctx, identity, false, func(v *SandboxInvocation) error {
		if v.ToolResult != nil {
			if !sameSandboxReceipt(v.ToolResult, r) {
				return ErrSandboxConflict
			}
			return nil
		}
		if !sandboxUID(v, r.PodUID) || v.ExecClaim == nil || !sandboxDigest(r.OutputDigest) || r.ObservedAt.IsZero() {
			return ErrSandboxTransition
		}
		v.ToolResult = &r
		return nil
	})
}

// AcceptToolResult reserves an internal immutable evaluation linkage for captured
// output after settlement; "accepted" here is NOT a passing gate or publication
// authority. Matching multiple tools may share an evaluation, but another attempt
// cannot. The parent writer must verify the complete required evidence set before
// publishing/cache writes; one linked result does not prove attempt completeness.
// Existing evaluation writers are deliberately unwired in this store slice.
func (s *SandboxJournal) AcceptToolResult(ctx context.Context, identity SandboxIdentity, evaluation uuid.UUID) error {
	return s.change(ctx, identity, true, func(v *SandboxInvocation) error {
		if evaluation == uuid.Nil || !v.Released || v.ToolResult == nil {
			return ErrSandboxTransition
		}
		if v.AcceptedEvaluation != nil && *v.AcceptedEvaluation != evaluation {
			return ErrSandboxConflict
		}
		v.AcceptedEvaluation = &evaluation
		return nil
	})
}
