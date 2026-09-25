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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/novaforge/novaforge/internal/gates"
)

func journalFor(t *testing.T, ceiling int) (*gates.SandboxJournal, *pgxpool.Pool, string) {
	t.Helper()
	pool := storePool(t)
	target := "target-" + uuid.NewString()
	j, err := gates.NewSandboxJournal(context.Background(), pool, target, "gate-sandbox", ceiling)
	if err != nil {
		t.Fatal(err)
	}
	return j, pool, target
}

func sandboxIntent() gates.SandboxIntent {
	return gates.SandboxIntent{InvocationID: uuid.New(), AttemptID: uuid.New(), RunID: uuid.New(), RepoID: uuid.New(), Gate: "tests", Tool: "go-test", SourceSHA: strings.Repeat("a", 40), PolicySHA: strings.Repeat("b", 40), SnapshotDigest: "sha256:" + strings.Repeat("c", 64), ImageDigest: "sha256:" + strings.Repeat("d", 64), CommandDigest: "sha256:" + strings.Repeat("e", 64), Container: "analysis", Containers: []string{"analysis"}}
}

func reserveSandbox(t *testing.T, j *gates.SandboxJournal, ctx context.Context) gates.SandboxIdentity {
	t.Helper()
	v, err := j.Reserve(ctx, sandboxIntent())
	if err != nil {
		t.Fatal(err)
	}
	return v.Identity
}

func sandboxOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func sandboxError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func sandboxCount(t *testing.T, pool *pgxpool.Pool, target string, want int) {
	t.Helper()
	var n int
	sandboxOK(t, pool.QueryRow(context.Background(), `SELECT reserved FROM gates.sandbox_capacity WHERE target=$1`, target).Scan(&n))
	if n != want {
		t.Fatalf("reserved=%d, want %d", n, want)
	}
}

func TestSandboxJournalExactIdentity(t *testing.T) {
	j, pool, target := journalFor(t, 2)
	ctx := scopedCtx(uuid.New())
	intent := sandboxIntent()
	original, err := j.Reserve(ctx, intent)
	sandboxOK(t, err)
	// Model caller-side acknowledgement loss: discard returned intent and reopen
	// the store. This exercises actual persisted state, not a mocked datastore.
	restarted, err := gates.NewSandboxJournal(context.Background(), pool, target, "gate-sandbox", 2)
	sandboxOK(t, err)
	retry, err := restarted.Reserve(ctx, intent)
	sandboxOK(t, err)
	if !reflect.DeepEqual(original, retry) {
		t.Fatal("exact readback changed intent")
	}
	sandboxCount(t, pool, target, 1)

	// Every bound identity dimension must reject mismatched readback/transition.
	for _, field := range []string{"InvocationID", "AttemptID", "RunID", "RepoID", "Gate", "Tool", "SourceSHA", "PolicySHA", "SnapshotDigest", "ImageDigest", "CommandDigest", "Container", "Containers", "OrgID", "Target", "Namespace", "PodName"} {
		t.Run(field, func(t *testing.T) {
			id := original.Identity
			f := reflect.ValueOf(&id).Elem().FieldByName(field)
			switch f.Kind() {
			case reflect.String:
				f.SetString(f.String() + "-different")
			case reflect.Array:
				f.Set(reflect.ValueOf(uuid.New()))
			case reflect.Slice:
				f.Set(reflect.ValueOf([]string{"injected"}))
			default:
				t.Fatal("unhandled identity type")
			}
			if _, err := j.ReadExact(ctx, id); err == nil {
				t.Fatal("mismatched identity read")
			}
			if _, err := j.ClaimCreate(ctx, id); err == nil {
				t.Fatal("mismatched identity dispatched")
			}
		})
	}
	intent.PolicySHA = strings.Repeat("f", 40)
	_, err = j.Reserve(ctx, intent)
	sandboxError(t, err, gates.ErrSandboxConflict)
	sandboxCount(t, pool, target, 1)
}

func TestSandboxJournalCrossOrg(t *testing.T) {
	j, pool, target := journalFor(t, 3)
	a, b := scopedCtx(uuid.New()), scopedCtx(uuid.New())
	id := reserveSandbox(t, j, a)
	if _, err := j.ReadExact(b, id); err == nil {
		t.Fatal("foreign identity read")
	}
	other := reserveSandbox(t, j, b)
	forged := id
	forged.OrgID = other.OrgID
	_, err := j.ReadExact(b, forged)
	sandboxError(t, err, pgx.ErrNoRows)
	_, err = j.ClaimCreate(b, forged)
	sandboxError(t, err, pgx.ErrNoRows)
	terminal, cleanup, absence := sandboxReceipts("foreign-uid")
	for _, err := range []error{
		j.BindPodUID(b, forged, "foreign-uid"),
		j.RecordTerminal(b, forged, terminal),
		j.RecordCleanup(b, forged, cleanup),
		j.RecordAbsence(b, forged, absence),
		j.RecordToolResult(b, forged, gates.SandboxToolResult{}),
		j.AcceptToolResult(b, forged, uuid.New()),
	} {
		sandboxError(t, err, pgx.ErrNoRows)
	}
	_, err = j.Reserve(b, id.SandboxIntent)
	sandboxError(t, err, gates.ErrSandboxConflict)
	sandboxCount(t, pool, target, 2)
	settled, err := j.FenceRuns(b, []uuid.UUID{id.RunID})
	sandboxOK(t, err)
	if !settled {
		t.Fatal("foreign obligation disclosed by fence")
	}
	_, err = j.ClaimCreate(a, id)
	sandboxOK(t, err)
	for _, ctx := range []context.Context{context.Background(), scopedCtx(uuid.Nil)} {
		if _, err := j.Reserve(ctx, sandboxIntent()); err == nil {
			t.Fatal("unscoped reservation")
		}
		if _, err := j.FenceOrganization(ctx); err == nil {
			t.Fatal("unscoped fence")
		}
	}
}

func TestSandboxJournalConcurrentCapacity(t *testing.T) {
	j, pool, target := journalFor(t, 5)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for n := 0; n < 24; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := j.Reserve(scopedCtx(uuid.New()), sandboxIntent())
			if err == nil {
				admitted.Add(1)
			} else if !errors.Is(err, gates.ErrSandboxCapacity) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 5 {
		t.Fatalf("admitted %d", admitted.Load())
	}
	sandboxCount(t, pool, target, 5)
	for _, ceiling := range []int{4, 6} {
		_, err := gates.NewSandboxJournal(context.Background(), pool, target, "gate-sandbox", ceiling)
		sandboxError(t, err, gates.ErrSandboxConflict)
	}
	_, err := gates.NewSandboxJournal(context.Background(), pool, target, "other-sandbox", 5)
	sandboxError(t, err, gates.ErrSandboxConflict)
	// Unknown obligations have no lease, deadline or API-object expiry release.
	restarted, err := gates.NewSandboxJournal(context.Background(), pool, target, "gate-sandbox", 5)
	sandboxOK(t, err)
	_, err = restarted.Reserve(scopedCtx(uuid.New()), sandboxIntent())
	sandboxError(t, err, gates.ErrSandboxCapacity)
}

func TestSandboxJournalConcurrentIdempotencyAndClaims(t *testing.T) {
	j, pool, target := journalFor(t, 1)
	ctx := scopedCtx(uuid.New())
	intent := sandboxIntent()
	var wg sync.WaitGroup
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := j.Reserve(ctx, intent)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	v, err := j.Reserve(ctx, intent)
	sandboxOK(t, err)
	sandboxCount(t, pool, target, 1)
	var claimed atomic.Int32
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, err := j.ClaimCreate(ctx, v.Identity)
			if err == nil {
				if claim == uuid.Nil {
					t.Error("empty acknowledged claim")
				}
				claimed.Add(1)
			} else if !errors.Is(err, gates.ErrSandboxClaimed) || claim != uuid.Nil {
				t.Errorf("claim=%s err=%v", claim, err)
			}
		}()
	}
	wg.Wait()
	if claimed.Load() != 1 {
		t.Fatalf("create dispatchers=%d", claimed.Load())
	}
	// A lost claim acknowledgement is deliberately unrecoverable as authority.
	restarted, err := gates.NewSandboxJournal(context.Background(), pool, target, "gate-sandbox", 1)
	sandboxOK(t, err)
	read, err := restarted.ReadExact(ctx, v.Identity)
	sandboxOK(t, err)
	if read.CreateClaim == nil {
		t.Fatal("claim not durable")
	}
	claim, err := restarted.ClaimCreate(ctx, v.Identity)
	sandboxError(t, err, gates.ErrSandboxClaimed)
	if claim != uuid.Nil {
		t.Fatal("replayed create authority")
	}
	sandboxOK(t, j.BindPodUID(ctx, v.Identity, "uid-original"))
	claimed.Store(0)
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := j.ClaimExec(ctx, v.Identity, "uid-original")
			if err == nil {
				claimed.Add(1)
			} else if !errors.Is(err, gates.ErrSandboxClaimed) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if claimed.Load() != 1 {
		t.Fatalf("exec dispatchers=%d", claimed.Load())
	}
}

func TestSandboxJournalCommandEncoding(t *testing.T) {
	a, err := gates.SandboxCommandDigest([]string{"echo", "a b"})
	sandboxOK(t, err)
	b, err := gates.SandboxCommandDigest([]string{"echo", "a", "b"})
	sandboxOK(t, err)
	if a == b {
		t.Fatal("ambiguous command encoding")
	}
	for _, argv := range [][]string{nil, {""}, {"echo", "bad\x00arg"}, {"\xff"}, {"echo", "\xff"}, {"echo", "\xfe"}, {"echo", "\xc3"}} {
		if digest, err := gates.SandboxCommandDigest(argv); !errors.Is(err, gates.ErrSandboxConflict) || digest != "" {
			t.Errorf("invalid command %q returned digest=%q err=%v", argv, digest, err)
		}
	}
	// Valid Unicode (including an actual replacement character) is allowed,
	// but distinct byte sequences must not be normalized to one identity.
	unicode, err := gates.SandboxCommandDigest([]string{"echo", "é", "\ufffd", "日本語"})
	sandboxOK(t, err)
	repeated, err := gates.SandboxCommandDigest([]string{"echo", "é", "\ufffd", "日本語"})
	sandboxOK(t, err)
	decomposed, err := gates.SandboxCommandDigest([]string{"echo", "e\u0301", "\ufffd", "日本語"})
	sandboxOK(t, err)
	if unicode == "" || unicode != repeated || unicode == decomposed {
		t.Fatal("valid Unicode command identity was lost or normalized")
	}
}
