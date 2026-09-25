package gates_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/gates"
)

func TestSandboxRecoveryPageChangesBetweenReads(t *testing.T) {
	j, pool, target := journalFor(t, 3)
	org := uuid.New()
	ctx := scopedCtx(org)
	firstIntent, secondIntent, lateIntent := sandboxIntent(), sandboxIntent(), sandboxIntent()
	// Preserve globally unique suffixes, while forcing one insertion behind the cursor.
	firstIntent.InvocationID[0] = 0x80
	secondIntent.InvocationID[0] = 0xc0
	lateIntent.InvocationID[0] = 0x40
	first, err := j.Reserve(ctx, firstIntent)
	sandboxOK(t, err)
	second, err := j.Reserve(ctx, secondIntent)
	sandboxOK(t, err)
	page, err := j.RecoveryPage(sandboxRecoveryContext(), gates.SandboxRecoveryRef{}, 1)
	sandboxOK(t, err)
	if len(page) != 1 || page[0].InvocationID != first.Identity.InvocationID {
		t.Fatalf("first page: %+v", page)
	}
	// Another owner settles the first row between page requests. Keyset pagination
	// must not skip the second row as an offset-based implementation would.
	_, err = j.ClaimCreate(ctx, first.Identity)
	sandboxOK(t, err)
	uid := uuid.NewString()
	sandboxOK(t, j.BindPodUID(ctx, first.Identity, uid))
	terminal, cleanup, absence := sandboxReceipts(uid)
	sandboxOK(t, j.RecordTerminal(ctx, first.Identity, terminal))
	sandboxOK(t, j.RecordCleanup(ctx, first.Identity, cleanup))
	sandboxOK(t, j.RecordAbsence(ctx, first.Identity, absence))
	next, err := j.RecoveryPage(sandboxRecoveryContext(), page[0], 1)
	sandboxOK(t, err)
	if len(next) != 1 || next[0].InvocationID != second.Identity.InvocationID {
		t.Fatalf("release shifted page: %+v", next)
	}
	late, err := j.Reserve(ctx, lateIntent)
	sandboxOK(t, err)
	end, err := j.RecoveryPage(sandboxRecoveryContext(), next[0], 1)
	sandboxOK(t, err)
	if len(end) != 0 {
		t.Fatalf("cursor moved backwards: %+v", end)
	}
	fresh, err := j.RecoveryPage(sandboxRecoveryContext(), gates.SandboxRecoveryRef{}, 3)
	sandboxOK(t, err)
	want := []gates.SandboxRecoveryRef{{OrgID: org, InvocationID: late.Identity.InvocationID}, {OrgID: org, InvocationID: second.Identity.InvocationID}}
	if !reflect.DeepEqual(fresh, want) {
		t.Fatalf("new sweep missed late insertion: %+v", fresh)
	}
	sandboxCount(t, pool, target, 2)
}
