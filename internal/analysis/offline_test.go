package analysis_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/novaforge/novaforge/internal/analysis"
	"github.com/novaforge/novaforge/internal/analysistest"
)

func TestVulnerabilitiesRejectsScannerErrorWithValidJSON(t *testing.T) {
	run := func(ctx context.Context, dir, name string, args ...string) ([]byte, int, error) {
		if name == "sh" && args[2] == "advisory-manifest" {
			raw, _ := json.Marshal(map[string]any{"schema_version": 1, "generated_at": time.Now().Add(-time.Hour), "valid_until": time.Now().Add(time.Hour), "ecosystems": map[string]string{"Go": strings.Repeat("a", 64)}})
			return raw, 0, nil
		}
		if name == "sh" {
			return []byte("100\n" + strings.Repeat("a", 64) + "  " + analysis.OfflineAdvisoryRoot + "/osv-scalibr/Go/all.zip\n"), 0, nil
		}
		return []byte(`{"results":[{"packages":[{"package":{"name":"golang.org/x/text","version":"0.3.0","ecosystem":"Go"}}]}]}`), 127, nil
	}
	if found, err := analysis.Vulnerabilities(context.Background(), run, t.TempDir()); err == nil {
		t.Fatalf("scanner failure reported clean: %+v", found)
	}
}

// Only test path translation differs from production. The image-owned ZIP is
// staged in an owned temporary directory; hashing and OSV matching are real.
func offlineFixtureExec(t *testing.T) analysis.Exec {
	run, _ := offlineFixture(t)
	return run
}

func offlineFixture(t *testing.T) (analysis.Exec, string) {
	return offlineFixtureWithExec(t, analysis.DefaultExec)
}

func offlineFixtureWithExec(t *testing.T, execute analysis.Exec) (analysis.Exec, string) {
	t.Helper()
	return analysistest.OfflineFixture(t, execute)
}

func TestOfflineVulnerabilitiesRejectsDamagedImage(t *testing.T) {
	for _, damage := range []string{"missing", "changed", "oversized", "oversized-manifest", "symlink-file", "symlink-directory", "symlink-parent", "expired", "future", "malformed-zip", "cancelled"} {
		t.Run(damage, func(t *testing.T) {
			run, root := offlineFixture(t)
			path := filepath.Join(root, "osv-scalibr", "Go", "all.zip")
			switch damage {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "changed":
				// A still-readable ZIP with altered bytes must fail its digest,
				// not merely rely on OSV rejecting malformed ZIP syntax.
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.Write([]byte("\n")); err != nil {
					f.Close()
					t.Fatal(err)
				}
				if err = f.Close(); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				f, err := os.OpenFile(path, os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.Truncate((512 << 20) + 1); err != nil {
					f.Close()
					t.Fatal(err)
				}
				if err = f.Close(); err != nil {
					t.Fatal(err)
				}
			case "oversized-manifest":
				if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(strings.Repeat(" ", (64<<10)+1)), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink-file":
				target := path + ".original"
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "symlink-directory", "symlink-parent":
				link := filepath.Dir(path)
				if damage == "symlink-parent" {
					link = filepath.Dir(link)
				}
				target := link + ".original"
				if err := os.Rename(link, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
			case "expired", "future", "malformed-zip":
				manifestPath := filepath.Join(root, "manifest.json")
				raw, err := os.ReadFile(manifestPath)
				if err != nil {
					t.Fatal(err)
				}
				var manifest map[string]any
				if err = json.Unmarshal(raw, &manifest); err != nil {
					t.Fatal(err)
				}
				if damage == "expired" {
					manifest["valid_until"] = time.Now().Add(-time.Second)
				}
				if damage == "future" {
					manifest["generated_at"] = time.Now().Add(time.Minute)
				}
				if damage == "malformed-zip" {
					raw = []byte("not a ZIP")
					if err = os.WriteFile(path, raw, 0600); err != nil {
						t.Fatal(err)
					}
					digest := sha256.Sum256(raw)
					manifest["ecosystems"] = map[string]string{"Go": hex.EncodeToString(digest[:])}
				}
				raw, err = json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(manifestPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if damage == "cancelled" {
				cancel()
			}
			dir := probe(t, map[string]string{"go.mod": "module probe.test/offline\n\ngo 1.22\n\nrequire golang.org/x/text v0.3.0\n"})
			if found, err := analysis.Vulnerabilities(ctx, run, dir); err == nil {
				t.Fatalf("%s image accepted: %+v", damage, found)
			}
		})
	}
}

func TestOfflineVulnerabilitiesKeepsWholeDependencyFindings(t *testing.T) {
	run := offlineFixtureExec(t)
	dir := probe(t, map[string]string{"go.mod": "module probe.test/offline\n\ngo 1.22\n\nrequire golang.org/x/text v0.3.0\n", "osv-scanner.toml": "[[IgnoredVulns]]\nid = \"GO-2021-0113\"\n"})
	var scannerCalls int
	checked := func(ctx context.Context, dir, name string, args ...string) ([]byte, int, error) {
		if name == "env" {
			scannerCalls++
			command := strings.Join(args, " ")
			for _, required := range []string{"--offline ", "--all-vulns", "--all-packages", "--no-call-analysis=go,rust", "--config /dev/null", "--no-ignore"} {
				if !strings.Contains(command, required) {
					return nil, 0, fmt.Errorf("missing %s", required)
				}
			}
			if strings.Contains(command, "download-offline") {
				t.Fatal("network update enabled")
			}
		}
		return run(ctx, dir, name, args...)
	}
	found, err := analysis.Vulnerabilities(context.Background(), checked, dir)
	if err != nil {
		t.Fatal(err)
	}
	if scannerCalls != 1 || len(found) == 0 {
		t.Fatalf("offline matching unavailable: calls=%d findings=%+v", scannerCalls, found)
	}
	var retained bool
	for _, finding := range found {
		for _, id := range finding.IDs {
			if id == "GO-2021-0113" {
				retained = true
			}
		}
	}
	if !retained {
		t.Fatal("repository-controlled advisory suppression was honored")
	}
	t.Logf("real offline scanner matched %d vulnerable dependencies", len(found))
}

func TestOfflineVulnerabilitiesWithNetworkDenied(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS network-denial fixture; Kubernetes production acceptance is separate")
	}
	requireTool(t, "sandbox-exec")
	const profile = "(version 1)(allow default)(deny network*)"
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Prove the local control server is reachable before testing the deny rule.
	_, exit, err := analysis.DefaultExec(ctx, t.TempDir(), "curl", "--silent", "--max-time", "2", server.URL)
	if err != nil || exit != 0 || hits.Load() != 1 {
		t.Fatalf("control listener unavailable: %d %v", exit, err)
	}
	isolated := func(ctx context.Context, dir, name string, args ...string) ([]byte, int, error) {
		command := append([]string{"-p", profile, name}, args...)
		return analysis.DefaultExec(ctx, dir, "sandbox-exec", command...)
	}
	_, exit, err = isolated(ctx, t.TempDir(), "curl", "--silent", "--max-time", "2", server.URL)
	if err != nil || exit == 0 || hits.Load() != 1 {
		t.Fatalf("network denial unproved: %d %v hits=%d", exit, err, hits.Load())
	}
	run, _ := offlineFixtureWithExec(t, isolated)
	for _, test := range []struct {
		version    string
		vulnerable bool
	}{{"0.3.0", true}, {"0.39.0", false}} {
		dir := probe(t, map[string]string{"go.mod": "module probe.test/offline\n\ngo 1.22\n\nrequire golang.org/x/text v" + test.version + "\n"})
		found, err := analysis.Vulnerabilities(ctx, run, dir)
		if err != nil {
			t.Fatal(err)
		}
		if (len(found) > 0) != test.vulnerable {
			t.Fatalf("v%s: %+v", test.version, found)
		}
	}
	if hits.Load() != 1 {
		t.Fatal("network-denied scanner contacted control listener")
	}
}
