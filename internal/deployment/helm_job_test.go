package deployment

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestHelmJobExecutesFixedCommandAndSanitizesEvidence(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	// A real controlled child process models only Helm's external contract. It
	// intentionally returns sensitive fields; none may reach the job log.
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$ARGV_LOG"
if [ "$1" = status ]; then exit 1; fi
printf '%s' '{"name":"fixture","namespace":"target","version":7,"info":{"status":"deployed","description":"binding"},"manifest":"DO-NOT-LOG-SECRET"}'
`
	path := filepath.Join(dir, "helm")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ARGV_LOG", log)
	var out bytes.Buffer
	err := RunHelmJob(context.Background(), []string{"execute", "fixture", "/charts/trusted.tgz", "target", "image.digest", "sha256:" + strings.Repeat("a", 64), "binding", "-"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "DO-NOT-LOG") || !strings.Contains(out.String(), `"revision":7`) {
		t.Fatalf("unsafe or missing evidence: %s", out.String())
	}
	argv, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"upgrade\n--install\nfixture\n/charts/trusted.tgz", "--wait\n", "--wait-for-jobs\n", "/credentials/config", "--description\nbinding"} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("missing fixed args %q in %s", want, argv)
		}
	}
}

func TestHelmJobDoesNotAcceptAnotherOperationOrTruncatedEvidence(t *testing.T) {
	for _, output := range []string{`{"name":"fixture","namespace":"target","version":7,"info":{"status":"deployed","description":"different"}}`, `{"name":"fixture"`} {
		t.Run(output, func(t *testing.T) {
			dir := t.TempDir()
			script := "#!/bin/sh\nprintf '%s' '" + output + "'\n"
			if err := os.WriteFile(filepath.Join(dir, "helm"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			var out bytes.Buffer
			if err := RunHelmJob(context.Background(), []string{"observe", "fixture", "/charts/trusted.tgz", "target", "image.digest", "sha256:" + strings.Repeat("a", 64), "binding", "-"}, &out); err == nil {
				t.Fatal("unbound or truncated Helm evidence accepted")
			}
		})
	}
}

func TestHelmOutputLimitCannotBeBypassedByReaderFrom(t *testing.T) {
	var output boundedOutput
	_, err := io.Copy(&output, io.LimitReader(strings.NewReader(strings.Repeat("x", (16<<20)+1)), (16<<20)+1))
	if err == nil {
		t.Fatal("Helm output limit bypassed by io.Copy")
	}
	if len(output.Bytes()) > 16<<20 {
		t.Fatal("oversized Helm output retained")
	}
}

func TestHelmJobDeliveryAttemptEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, mode, statusBinding, statusState, upgradeBinding, wantState string
		upgradeFails                                                      bool
		wantUpgrades                                                      int
	}{
		{name: "observe old failed revision", mode: "observe", statusBinding: "previous", statusState: "failed", wantState: StateUncertain},
		{name: "retry transport failure leaves old revision", mode: "execute", statusBinding: "previous", statusState: "failed", upgradeFails: true, wantState: StateUncertain, wantUpgrades: 1},
		{name: "retry writes its own revision", mode: "execute", statusBinding: "previous", statusState: "failed", upgradeBinding: "current", wantState: StateSucceeded, wantUpgrades: 1},
		{name: "failed current attempt never reexecutes", mode: "execute", statusBinding: "current", statusState: "failed", wantState: StateFailed},
		{name: "cannot overwrite unrelated revision", mode: "execute", statusBinding: "other", statusState: "failed", wantState: StateUncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			script := `#!/bin/sh
if [ "$1" = status ]; then
 printf '{"name":"fixture","namespace":"target","version":1,"info":{"status":"%s","description":"%s"}}' "$STATUS_STATE" "$STATUS_BINDING"
 exit 0
fi
printf 'upgrade\n' >> "$CALLS"
if [ "$UPGRADE_FAILS" = true ]; then exit 1; fi
printf '{"name":"fixture","namespace":"target","version":2,"info":{"status":"deployed","description":"%s"}}' "$UPGRADE_BINDING"
`
			if err := os.WriteFile(filepath.Join(dir, "helm"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CALLS", log)
			t.Setenv("STATUS_STATE", tc.statusState)
			t.Setenv("STATUS_BINDING", tc.statusBinding)
			t.Setenv("UPGRADE_BINDING", tc.upgradeBinding)
			t.Setenv("UPGRADE_FAILS", strconv.FormatBool(tc.upgradeFails))
			var out bytes.Buffer
			err := RunHelmJob(context.Background(), []string{tc.mode, "fixture", "/charts/trusted.tgz", "target", "image.digest", "sha256:" + strings.Repeat("a", 64), "current", "previous"}, &out)
			var evidence HelmEvidence
			if json.Unmarshal(out.Bytes(), &evidence) != nil || evidence.State != tc.wantState || (err == nil) != (tc.wantState == StateSucceeded) {
				t.Fatalf("attempt evidence: %s %v", out.String(), err)
			}
			calls, readErr := os.ReadFile(log)
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			if strings.Count(string(calls), "upgrade\n") != tc.wantUpgrades {
				t.Fatalf("unexpected upgrades: %s", calls)
			}
		})
	}
}
