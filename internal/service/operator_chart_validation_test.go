package service_test

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOperatorChartRejectsAmbiguousConfiguration(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal("helm required for operator mount acceptance")
	}
	cases := map[string]string{
		"mutable analysis image": "services.gates.analysisImage=registry.example/analysis:latest",
		"unknown feature":        "operatorConfigs.typo.secretName=owned",
		"misspelled field":       "operatorConfigs.openbao.secret_name=owned",
		"path traversal":         "operatorConfigs.openbao.configKey=../credential",
		"dot path":               "operatorConfigs.openbao.configKey=..",
		"invalid secret name":    "operatorConfigs.openbao.secretName=Owned",
	}
	for name, setting := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := exec.Command(helm, "template", "nftest", filepath.Join(repoRoot(t), "deploy/helm/novaforge"), "--set-string", setting).CombinedOutput()
			if err == nil {
				t.Fatalf("ambiguous operator configuration was accepted: %s (rendered %d bytes)", setting, len(out))
			}
		})
	}
}
