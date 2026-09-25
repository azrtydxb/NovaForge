package gates_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/gates"
)

func sandboxReceipts(uid string) (gates.SandboxTerminalReceipt, gates.SandboxCleanupReceipt, gates.SandboxAbsenceReceipt) {
	now := time.Now().UTC()
	terminal := gates.SandboxTerminalReceipt{PodUID: uid, Phase: "Succeeded", ObservedAt: now, Containers: []gates.SandboxContainerTerminal{{Name: "analysis", ExitCode: 0, FinishedAt: now.Add(-time.Second)}}}
	cleanup := gates.SandboxCleanupReceipt{PodUID: uid, DeleteID: uuid.New(), AcknowledgedAt: now.Add(time.Second)}
	absence := gates.SandboxAbsenceReceipt{PodUID: uid, DeleteID: cleanup.DeleteID, ObservedAt: now.Add(2 * time.Second)}
	return terminal, cleanup, absence
}

func TestSandboxJournalLifecycle(t *testing.T) {
	j, pool, target := journalFor(t, 1)
	ctx := scopedCtx(uuid.New())
	id := reserveSandbox(t, j, ctx)
	uid := uuid.NewString()
	terminal, cleanup, absence := sandboxReceipts(uid)
	sandboxError(t, j.BindPodUID(ctx, id, uid), gates.ErrSandboxTransition)
	_, err := j.ClaimExec(ctx, id, uid)
	sandboxError(t, err, gates.ErrSandboxTransition)
	_, err = j.ClaimCreate(ctx, id)
	sandboxOK(t, err)
	// NotFound after uncertain Create is not a terminal or absence receipt.
	sandboxError(t, j.RecordAbsence(ctx, id, absence), gates.ErrSandboxTransition)
	sandboxError(t, j.RecordTerminal(ctx, id, terminal), gates.ErrSandboxTransition)
	sandboxOK(t, j.BindPodUID(ctx, id, uid))
	// Discard UID acknowledgement and reopen: only exact durable UID survives.
	j, err = gates.NewSandboxJournal(context.Background(), pool, target, "gate-sandbox", 1)
	sandboxOK(t, err)
	sandboxOK(t, j.BindPodUID(ctx, id, uid))
	sandboxError(t, j.BindPodUID(ctx, id, "replacement"), gates.ErrSandboxConflict)
	_, err = j.ClaimExec(ctx, id, "replacement")
	sandboxError(t, err, gates.ErrSandboxTransition)
	_, err = j.ClaimExec(ctx, id, uid)
	sandboxOK(t, err)
	_, err = j.ClaimExec(ctx, id, uid)
	sandboxError(t, err, gates.ErrSandboxClaimed)
	sandboxError(t, j.RecordCleanup(ctx, id, cleanup), gates.ErrSandboxTransition)
	sandboxError(t, j.RecordAbsence(ctx, id, absence), gates.ErrSandboxTransition)
	sandboxError(t, j.AcceptToolResult(ctx, id, uuid.New()), gates.ErrSandboxTransition)

	wrong := terminal
	wrong.PodUID = "replacement"
	sandboxError(t, j.RecordTerminal(ctx, id, wrong), gates.ErrSandboxTransition)
	wrong = terminal
	wrong.Containers = append(append([]gates.SandboxContainerTerminal{}, terminal.Containers...), gates.SandboxContainerTerminal{Name: "injected", FinishedAt: terminal.ObservedAt})
	sandboxError(t, j.RecordTerminal(ctx, id, wrong), gates.ErrSandboxTransition)
	wrong = terminal
	wrong.Containers = []gates.SandboxContainerTerminal{{Name: "analysis"}}
	sandboxError(t, j.RecordTerminal(ctx, id, wrong), gates.ErrSandboxTransition)
	sandboxOK(t, j.RecordTerminal(ctx, id, terminal))
	sandboxOK(t, j.RecordTerminal(ctx, id, terminal))
	wrong = terminal
	wrong.Phase = "Failed"
	sandboxError(t, j.RecordTerminal(ctx, id, wrong), gates.ErrSandboxConflict)
	// Terminal-but-undeleted and absent-before-cleanup both retain the slot.
	_, err = j.Reserve(ctx, sandboxIntent())
	sandboxError(t, err, gates.ErrSandboxCapacity)
	sandboxError(t, j.RecordAbsence(ctx, id, absence), gates.ErrSandboxTransition)
	sandboxOK(t, j.RecordCleanup(ctx, id, cleanup))
	sandboxCount(t, pool, target, 1)
	badCleanup := cleanup
	badCleanup.DeleteID = uuid.New()
	sandboxError(t, j.RecordCleanup(ctx, id, badCleanup), gates.ErrSandboxConflict)
	badAbsence := absence
	badAbsence.PodUID = "replacement"
	sandboxError(t, j.RecordAbsence(ctx, id, badAbsence), gates.ErrSandboxTransition)
	badAbsence = absence
	badAbsence.DeleteID = uuid.New()
	sandboxError(t, j.RecordAbsence(ctx, id, badAbsence), gates.ErrSandboxTransition)

	// Concurrent recovery receipts can release once, never underflow capacity.
	var wg sync.WaitGroup
	for n := 0; n < 10; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := j.RecordAbsence(ctx, id, absence); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	sandboxCount(t, pool, target, 0)
	sandboxError(t, j.RecordAbsence(ctx, id, badAbsence), gates.ErrSandboxConflict)
	v, err := j.ReadExact(ctx, id)
	sandboxOK(t, err)
	if !v.Released || v.Terminal == nil || v.Absence == nil || v.ToolResult != nil || v.AcceptedEvaluation != nil {
		t.Fatal("lifecycle recovery fabricated output or lost receipts")
	}
	sandboxError(t, j.AcceptToolResult(ctx, id, uuid.New()), gates.ErrSandboxTransition)
	result := gates.SandboxToolResult{PodUID: uid, OutputDigest: "sha256:" + strings.Repeat("f", 64), ExitCode: 1, ObservedAt: terminal.ObservedAt}
	sandboxOK(t, j.RecordToolResult(ctx, id, result))
	sandboxOK(t, j.RecordToolResult(ctx, id, result))
	conflict := result
	conflict.ExitCode = 0
	sandboxError(t, j.RecordToolResult(ctx, id, conflict), gates.ErrSandboxConflict)
	evaluation := uuid.New()
	sandboxOK(t, j.AcceptToolResult(ctx, id, evaluation))
	sandboxOK(t, j.AcceptToolResult(ctx, id, evaluation))
	sandboxError(t, j.AcceptToolResult(ctx, id, uuid.New()), gates.ErrSandboxConflict)
	v, err = j.ReadExact(ctx, id)
	sandboxOK(t, err)
	if v.ToolResult.ExitCode != 1 || *v.AcceptedEvaluation != evaluation {
		t.Fatal("terminal success replaced actual tool outcome")
	}
	// Releasing capacity never discards/recycles the old name/immutable evidence.
	newID := reserveSandbox(t, j, ctx)
	if newID.PodName == id.PodName {
		t.Fatal("invocation name reused")
	}
	_, err = j.Reserve(ctx, id.SandboxIntent)
	sandboxOK(t, err)
	sandboxCount(t, pool, target, 1)
}

func TestSandboxJournalUnknownRetainsCapacity(t *testing.T) {
	j, pool, target := journalFor(t, 1)
	ctx := scopedCtx(uuid.New())
	id := reserveSandbox(t, j, ctx)
	_, err := j.ClaimCreate(ctx, id)
	sandboxOK(t, err)
	_, cleanup, absence := sandboxReceipts("late-pod")
	for n := 0; n < 3; n++ {
		// Repeated missing/API-deleted/expired observations carry no termination
		// proof. There is intentionally no expiry or force-release method.
		sandboxError(t, j.RecordAbsence(ctx, id, absence), gates.ErrSandboxTransition)
		_, err = j.Reserve(ctx, sandboxIntent())
		sandboxError(t, err, gates.ErrSandboxCapacity)
	}
	// Delayed Create can still bind after initial NotFound; exec remains unused.
	sandboxOK(t, j.BindPodUID(ctx, id, "late-pod"))
	sandboxError(t, j.RecordCleanup(ctx, id, cleanup), gates.ErrSandboxTransition)
	sandboxError(t, j.RecordAbsence(ctx, id, absence), gates.ErrSandboxTransition)
	sandboxCount(t, pool, target, 1)
	settled, err := j.FenceOrganization(ctx)
	sandboxOK(t, err)
	if settled {
		t.Fatal("unknown creation cleared purge")
	}
	v, err := j.ReadExact(ctx, id)
	sandboxOK(t, err)
	if v.Released || v.ToolResult != nil {
		t.Fatal("unknown lost obligation")
	}
}
