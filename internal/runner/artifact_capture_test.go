package runner_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/novaforge/novaforge/internal/runner"
)

func TestArtifactCapturePreservesPathsAndFailsOnMissing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		paths []string
		fail  bool
	}{
		{"spaces", []string{"a report.txt"}, false},
		{"partially missing", []string{"a report.txt", "missing.txt"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "a report.txt"), []byte("evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", "true\n"+runner.ArtifactCaptureScriptForTest(tc.paths))
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if tc.fail {
				if err == nil {
					t.Fatalf("capture succeeded without all declared paths: %s", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("capture: %v: %s", err, out)
			}
			arts, _, err := runner.ExtractArtifactsForTest(strings.Split(strings.TrimSpace(string(out)), "\n"))
			if err != nil || len(arts) != 1 || arts[0].Name != "a report.txt" || string(arts[0].Content) != "evidence" {
				t.Fatalf("captured = %v, %v", arts, err)
			}
		})
	}
}
