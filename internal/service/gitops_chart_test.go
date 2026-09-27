package service_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitOpsChartPreservesOperatorCredentialsAndAuthority(t *testing.T) {
	args := []string{"template", "novaforge", filepath.Join(repoRoot(t), "deploy/helm/novaforge"), "--set", "secrets.existingSecret=operator-secrets", "--set", "rbac.create=false", "--set", "storage.pruneProtection=true", "--set", "operatorConfigs.semanticProducer.secretName=producer-policy", "--set", "runner.orgId=fixture", "--set", "secrets.postgresPassword=gitops-must-not-render"}
	raw, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v %s", err, raw)
	}
	objects := decodeManifests(t, raw)
	claims := 0
	for _, o := range objects {
		switch o.Kind {
		case "Secret", "ServiceAccount", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding":
			t.Errorf("GitOps rendered operator-owned %s", o.Kind)
		}
		if o.Kind == "PersistentVolumeClaim" {
			claims++
		}
	}
	rendered := string(raw)
	for _, literal := range []string{"change-me-in-production", "gitops-must-not-render", "value: \"minioadmin\""} {
		if strings.Contains(rendered, literal) {
			t.Errorf("credential literal rendered: %s", literal)
		}
	}
	if strings.Count(rendered, "name: operator-secrets") != 22 {
		t.Errorf("all app/runner envFrom, model-key and datastore references must use operator secret")
	}
	if claims != 3 || strings.Count(rendered, "sync.kuvryn.io/prune: disabled") != 3 {
		t.Errorf("all three claims require prune protection")
	}
}
