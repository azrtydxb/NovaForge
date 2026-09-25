package service_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGateRejectsGenericClusterRBAC(t *testing.T) {
	out, err := exec.Command("helm", "template", "nftest", filepath.Join(repoRoot(t), "deploy/helm/novaforge"),
		"--set", "services.gates.rbac=true").CombinedOutput()
	if err == nil {
		t.Fatal("Gates accepted the generic cluster-wide role")
	}
	if !strings.Contains(string(out), "Gates cannot use generic cluster RBAC") {
		t.Fatalf("render failed for an unrelated reason: %v: %s", err, out)
	}
}
