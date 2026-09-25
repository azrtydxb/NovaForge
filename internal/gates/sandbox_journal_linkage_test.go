package gates_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/gates"
)

func settleSandboxResult(t *testing.T, j *gates.SandboxJournal, ctx context.Context, id gates.SandboxIdentity) {
	t.Helper()
	uid := uuid.NewString()
	_, err := j.ClaimCreate(ctx, id)
	sandboxOK(t, err)
	sandboxOK(t, j.BindPodUID(ctx, id, uid))
	_, err = j.ClaimExec(ctx, id, uid)
	sandboxOK(t, err)
	terminal, cleanup, absence := sandboxReceipts(uid)
	sandboxOK(t, j.RecordToolResult(ctx, id, gates.SandboxToolResult{PodUID: uid, OutputDigest: "sha256:" + strings.Repeat("f", 64), ObservedAt: terminal.ObservedAt}))
	sandboxError(t, j.AcceptToolResult(ctx, id, uuid.New()), gates.ErrSandboxTransition)
	sandboxOK(t, j.RecordTerminal(ctx, id, terminal))
	sandboxOK(t, j.RecordCleanup(ctx, id, cleanup))
	sandboxOK(t, j.RecordAbsence(ctx, id, absence))
}

func TestSandboxJournalAttemptIdentity(t *testing.T) {
	j, pool, target := journalFor(t, 20)
	ctx := scopedCtx(uuid.New())
	id := reserveSandbox(t, j, ctx)
	for _, field := range []string{"RunID", "RepoID", "Gate", "SourceSHA", "PolicySHA", "SnapshotDigest", "ImageDigest"} {
		t.Run(field, func(t *testing.T) {
			intent := id.SandboxIntent
			intent.InvocationID = uuid.New()
			f := reflect.ValueOf(&intent).Elem().FieldByName(field)
			switch f.Kind() {
			case reflect.Array:
				f.Set(reflect.ValueOf(uuid.New()))
			case reflect.String:
				if strings.HasSuffix(field, "SHA") {
					f.SetString(strings.Repeat("f", 40))
				} else if strings.HasSuffix(field, "Digest") {
					f.SetString("sha256:" + strings.Repeat("f", 64))
				} else {
					f.SetString("other-gate")
				}
			}
			_, err := j.Reserve(ctx, intent)
			sandboxError(t, err, gates.ErrSandboxConflict)
			sandboxCount(t, pool, target, 1)
		})
	}
	other, err := gates.NewSandboxJournal(context.Background(), pool, "other-"+uuid.NewString(), "other-sandbox", 20)
	sandboxOK(t, err)
	intent := id.SandboxIntent
	intent.InvocationID = uuid.New()
	_, err = other.Reserve(ctx, intent)
	sandboxError(t, err, gates.ErrSandboxConflict)
	_, err = j.Reserve(scopedCtx(uuid.New()), intent)
	sandboxError(t, err, gates.ErrSandboxConflict)
	sandboxCount(t, pool, target, 1)
}

func TestSandboxJournalMultiToolLinkage(t *testing.T) {
	j, pool, target := journalFor(t, 5)
	ctx := scopedCtx(uuid.New())
	first := reserveSandbox(t, j, ctx)
	intent := first.SandboxIntent
	intent.InvocationID, intent.Tool, intent.CommandDigest = uuid.New(), "lint", "sha256:"+strings.Repeat("f", 64)
	second, err := j.Reserve(ctx, intent)
	sandboxOK(t, err)
	if second.Identity.PodName == first.PodName {
		t.Fatal("multiple tools shared execution slot")
	}
	settleSandboxResult(t, j, ctx, first)
	settleSandboxResult(t, j, ctx, second.Identity)
	evaluation := uuid.New()
	sandboxOK(t, j.AcceptToolResult(ctx, first, evaluation))
	sandboxOK(t, j.AcceptToolResult(ctx, second.Identity, evaluation))
	sandboxOK(t, j.AcceptToolResult(ctx, second.Identity, evaluation))
	sandboxCount(t, pool, target, 0)
	// Another attempt cannot piggyback even if all other dimensions match.
	intent.InvocationID, intent.AttemptID = uuid.New(), uuid.New()
	third, err := j.Reserve(ctx, intent)
	sandboxOK(t, err)
	settleSandboxResult(t, j, ctx, third.Identity)
	sandboxError(t, j.AcceptToolResult(ctx, third.Identity, evaluation), gates.ErrSandboxConflict)
	v, err := j.ReadExact(ctx, third.Identity)
	sandboxOK(t, err)
	if v.AcceptedEvaluation != nil {
		t.Fatal("conflicting link partially committed")
	}
	// A valid settled foreign invocation cannot reuse this evaluation ID either.
	foreignCtx := scopedCtx(uuid.New())
	foreign := reserveSandbox(t, j, foreignCtx)
	settleSandboxResult(t, j, foreignCtx, foreign)
	sandboxError(t, j.AcceptToolResult(foreignCtx, foreign, evaluation), gates.ErrSandboxConflict)
}

func TestSandboxJournalConcurrentLinkage(t *testing.T) {
	j, pool, target := journalFor(t, 5)
	ctx := scopedCtx(uuid.New())
	first := sandboxIntent()
	second := first
	second.InvocationID, second.PolicySHA = uuid.New(), strings.Repeat("f", 40)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, intent := range []gates.SandboxIntent{first, second} {
		wg.Add(1)
		go func(intent gates.SandboxIntent) {
			defer wg.Done()
			<-start
			_, err := j.Reserve(ctx, intent)
			if err == nil {
				admitted.Add(1)
			} else if !errors.Is(err, gates.ErrSandboxConflict) {
				t.Error(err)
			}
		}(intent)
	}
	close(start)
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("conflicting attempt registrations=%d", admitted.Load())
	}
	sandboxCount(t, pool, target, 1)
	a, b := reserveSandbox(t, j, ctx), reserveSandbox(t, j, ctx)
	settleSandboxResult(t, j, ctx, a)
	settleSandboxResult(t, j, ctx, b)
	evaluation := uuid.New()
	admitted.Store(0)
	for _, id := range []gates.SandboxIdentity{a, b} {
		wg.Add(1)
		go func(id gates.SandboxIdentity) {
			defer wg.Done()
			err := j.AcceptToolResult(ctx, id, evaluation)
			if err == nil {
				admitted.Add(1)
			} else if !errors.Is(err, gates.ErrSandboxConflict) {
				t.Error(err)
			}
		}(id)
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("conflicting evaluation registrations=%d", admitted.Load())
	}
}
