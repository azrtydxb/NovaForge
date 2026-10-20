package service_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// kwGoldenSets pins the two values hack/promote-kw.sh rewrites on every
// promotion, so a promotion does not change the golden and anything else does.
var kwGoldenSets = []string{
	"--set", "image.tag=golden",
	"--set", "services.gates.analysisImage=192.168.10.131/novaforge/gate-analysis@sha256:0000000000000000000000000000000000000000000000000000000000000000",
}

// The kw release is driven by Kuvryn Sync from values-kw.yaml, so a chart
// change meant for another cluster must not move a single byte of kw's render
// unless that is the point of the change. The golden is the render of
// values-kw.yaml as Sync produces it (release novaforge, namespace novaforge).
//
// A deliberate change to kw regenerates it, and the diff is reviewed with the
// change:  NF_UPDATE_GOLDEN=1 go test ./internal/service/ -run TestKwRenderIsUnchanged
func TestKwRenderIsUnchanged(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed; the chart cannot be rendered")
	}
	chart := filepath.Join(repoRoot(t), "deploy", "helm", "novaforge")
	args := append([]string{"template", "novaforge", chart, "-n", "novaforge", "-f", filepath.Join(chart, "values-kw.yaml")}, kwGoldenSets...)
	cmd := exec.Command(helm, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template -f values-kw.yaml: %v\n%s", err, stderr.String())
	}
	// Helm prints the chart path in "# Source:" comments relative to the chart
	// name, so the render does not depend on where the checkout lives.
	golden := filepath.Join(repoRoot(t), "internal", "service", "testdata", "kw-render.golden.yaml")
	if os.Getenv("NF_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, out, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(out, want) {
		return
	}
	got, wantLines := strings.Split(string(out), "\n"), strings.Split(string(want), "\n")
	for i := 0; i < len(got) || i < len(wantLines); i++ {
		var g, w string
		if i < len(got) {
			g = got[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w {
			t.Fatalf("the kw render changed (first difference at line %d):\n  golden: %q\n  now:    %q\nIf the change to kw is deliberate, regenerate with NF_UPDATE_GOLDEN=1 and review the diff.", i+1, w, g)
		}
	}
}
