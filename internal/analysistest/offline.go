// Package analysistest stages explicit advisory fixtures for real-tool tests.
// It is not a production fallback or evidence of advisory-data freshness.
package analysistest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/novaforge/novaforge/internal/analysis"
)

// OfflineFixture maps the image's fixed advisory paths to an owned temporary
// directory. Advisory matching and integrity checks still use the real tools;
// only paths and manifest validity times are test-controlled. Nothing downloads.
func OfflineFixture(t testing.TB, execute analysis.Exec) (analysis.Exec, string) {
	t.Helper()
	source := os.Getenv("NF_TEST_OSV_GO_ZIP")
	if source == "" {
		t.Fatal("NF_TEST_OSV_GO_ZIP must name the explicitly staged OSV Go/all.zip; offline tests do not download")
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "osv-scalibr", "Go"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "osv-scalibr", "Go", "all.zip"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	manifest, err := json.Marshal(map[string]any{"schema_version": 1, "generated_at": time.Now().UTC().Add(-time.Minute), "valid_until": time.Now().UTC().Add(time.Hour), "ecosystems": map[string]string{"Go": hex.EncodeToString(digest[:])}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	scanner, err := exec.LookPath("osv-scanner")
	if err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, dir, name string, args ...string) ([]byte, int, error) {
		mapped := append([]string(nil), args...)
		for i, arg := range mapped {
			mapped[i] = strings.ReplaceAll(arg, analysis.OfflineAdvisoryRoot, root)
			if arg == "/opt" || arg == "/opt/analysis" {
				mapped[i] = root
			}
			if arg == "osv-scanner" {
				mapped[i] = scanner
			}
		}
		out, exit, err := execute(ctx, dir, name, mapped...)
		if name == "sh" {
			out = []byte(strings.ReplaceAll(string(out), root, analysis.OfflineAdvisoryRoot))
		}
		return out, exit, err
	}, root
}
