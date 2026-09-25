package gates_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/gates"
)

func TestSandboxJournalTextIdentityRoundTrips(t *testing.T) {
	for _, tc := range []struct{ name, gate, tool string }{
		{"gate-ff", "tests-\xff", "go-test"},
		{"gate-fe", "tests-\xfe", "go-test"},
		{"tool-ff", "tests", "go-\xff"},
		{"tool-fe", "tests", "go-\xfe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			journal, pool, target := journalFor(t, 1)
			ctx := scopedCtx(uuid.New())
			intent := sandboxIntent()
			intent.Gate, intent.Tool = tc.gate, tc.tool
			if _, err := journal.Reserve(ctx, intent); !errors.Is(err, gates.ErrSandboxConflict) {
				t.Errorf("lossy JSON identity admitted: %v", err)
			}
			sandboxCount(t, pool, target, 0)
		})
	}
	t.Run("valid-unicode", func(t *testing.T) {
		journal, pool, target := journalFor(t, 1)
		ctx := scopedCtx(uuid.New())
		intent := sandboxIntent()
		intent.Gate, intent.Tool = "tests-東京", "outil-é"
		reserved, err := journal.Reserve(ctx, intent)
		sandboxOK(t, err)
		readback, err := journal.ReadExact(ctx, reserved.Identity)
		sandboxOK(t, err)
		if !reflect.DeepEqual(readback.Identity, reserved.Identity) {
			t.Fatal("valid Unicode identity changed on persistence")
		}
		sandboxCount(t, pool, target, 1)
	})
}
