package gates_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/gates"
)

func sandboxRecoveryContext() context.Context {
	return authz.WithScope(context.Background(), authz.Scope{ActorKind: "service", PlatformWorker: "gates-sandbox-recovery"})
}

func TestSandboxRecoveryEnumerationAuthority(t *testing.T) {
	j, _, _ := journalFor(t, 3)
	org := uuid.New()
	id := reserveSandbox(t, j, scopedCtx(org))
	bad := []context.Context{context.Background(), scopedCtx(org), authz.WithScope(context.Background(), authz.Scope{ActorKind: "service", PlatformWorker: "another-worker"}), authz.WithScope(context.Background(), authz.Scope{ActorKind: "user", PlatformWorker: "gates-sandbox-recovery"}), authz.WithScope(context.Background(), authz.Scope{ActorKind: "service", PlatformWorker: "gates-sandbox-recovery", OrgID: org})}
	for i, ctx := range bad {
		if rows, err := j.RecoveryPage(ctx, gates.SandboxRecoveryRef{}, 1); err == nil || len(rows) != 0 {
			t.Fatalf("scope %d enumerated tenant IDs: %v %v", i, rows, err)
		}
	}
	for _, limit := range []int{-1, 0, 129} {
		if _, err := j.RecoveryPage(sandboxRecoveryContext(), gates.SandboxRecoveryRef{}, limit); err == nil {
			t.Fatalf("unbounded page limit accepted: %d", limit)
		}
	}
	if _, err := j.RecoveryPage(sandboxRecoveryContext(), gates.SandboxRecoveryRef{OrgID: org}, 1); err == nil {
		t.Fatal("partial cursor accepted")
	}
	rows, err := j.RecoveryPage(sandboxRecoveryContext(), gates.SandboxRecoveryRef{}, 1)
	sandboxOK(t, err)
	if !reflect.DeepEqual(rows, []gates.SandboxRecoveryRef{{OrgID: org, InvocationID: id.InvocationID}}) {
		t.Fatalf("unexpected identifiers: %+v", rows)
	}
	// Enumeration authority cannot read tenant payloads until re-entering scope.
	if _, err := j.ReadForRecovery(sandboxRecoveryContext(), id.InvocationID); err == nil {
		t.Fatal("platform scope read tenant identity")
	}
	if _, err := j.ReadForRecovery(scopedCtx(uuid.New()), id.InvocationID); err == nil {
		t.Fatal("foreign org read recovery identity")
	}
	got, err := j.ReadForRecovery(scopedCtx(org), id.InvocationID)
	sandboxOK(t, err)
	if !reflect.DeepEqual(got.Identity, id) {
		t.Fatal("recovery identity changed")
	}
	if got.CreateClaim != nil || got.ExecClaim != nil {
		t.Fatal("read granted dispatch authority")
	}
}

func TestSandboxRecoveryPagesRetainFencedObligations(t *testing.T) {
	j, pool, target := journalFor(t, 5)
	a, b := uuid.New(), uuid.New()
	if a.String() > b.String() {
		a, b = b, a
	}
	invocations := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	if invocations[0].String() > invocations[1].String() {
		invocations[0], invocations[1] = invocations[1], invocations[0]
	}
	ctxA, ctxB := scopedCtx(a), scopedCtx(b)
	ids := []gates.SandboxIdentity{}
	for n, ctx := range []context.Context{ctxA, ctxA, ctxB} {
		intent := sandboxIntent()
		intent.InvocationID = invocations[n]
		v, err := j.Reserve(ctx, intent)
		sandboxOK(t, err)
		ids = append(ids, v.Identity)
	}
	other, err := gates.NewSandboxJournal(context.Background(), pool, target+"-other", "gate-sandbox", 1)
	sandboxOK(t, err)
	reserveSandbox(t, other, ctxA)
	if _, err := other.ReadForRecovery(ctxA, ids[0].InvocationID); err == nil {
		t.Fatal("target mapping redirected recovery")
	}
	// Settle only the middle row through the actual journal transitions.
	id := ids[1]
	_, err = j.ClaimCreate(ctxA, id)
	sandboxOK(t, err)
	uid := uuid.NewString()
	sandboxOK(t, j.BindPodUID(ctxA, id, uid))
	_, err = j.ClaimExec(ctxA, id, uid)
	sandboxOK(t, err)
	claimed, err := j.ReadForRecovery(ctxA, id.InvocationID)
	sandboxOK(t, err)
	if claimed.CreateClaim == nil || claimed.ExecClaim == nil {
		t.Fatal("recovery lost historical dispatch claims")
	}
	_, err = j.ClaimCreate(ctxA, claimed.Identity)
	sandboxError(t, err, gates.ErrSandboxClaimed)
	_, err = j.ClaimExec(ctxA, claimed.Identity, uid)
	sandboxError(t, err, gates.ErrSandboxClaimed)
	terminal, cleanup, absence := sandboxReceipts(uid)
	sandboxOK(t, j.RecordTerminal(ctxA, id, terminal))
	sandboxOK(t, j.RecordCleanup(ctxA, id, cleanup))
	sandboxOK(t, j.RecordAbsence(ctxA, id, absence))
	released, err := j.ReadForRecovery(ctxA, id.InvocationID)
	sandboxOK(t, err)
	if !released.Released || released.CreateClaim == nil || released.ExecClaim == nil || released.ToolResult != nil || released.AcceptedEvaluation != nil {
		t.Fatal("released readback lost history or fabricated output")
	}
	// Deletion fences must not make unresolved cleanup disappear.
	_, err = j.FenceOrganization(ctxA)
	sandboxOK(t, err)
	reopened, err := gates.NewSandboxJournal(context.Background(), pool, target, "gate-sandbox", 5)
	sandboxOK(t, err)
	first, err := reopened.RecoveryPage(sandboxRecoveryContext(), gates.SandboxRecoveryRef{}, 1)
	sandboxOK(t, err)
	if len(first) != 1 {
		t.Fatalf("first page: %v", first)
	}
	second, err := reopened.RecoveryPage(sandboxRecoveryContext(), first[0], 1)
	sandboxOK(t, err)
	want := []gates.SandboxRecoveryRef{{OrgID: a, InvocationID: ids[0].InvocationID}, {OrgID: b, InvocationID: ids[2].InvocationID}}
	if !reflect.DeepEqual(append(first, second...), want) {
		t.Fatalf("recovery skipped/duplicated/mixed obligations: %v %v", first, second)
	}
	end, err := reopened.RecoveryPage(sandboxRecoveryContext(), second[0], 1)
	sandboxOK(t, err)
	if len(end) != 0 {
		t.Fatal("page repeated completed obligations")
	}
	sandboxCount(t, pool, target, 2)
}
