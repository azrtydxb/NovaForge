package service_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGateAnalysisImageIsRenderedOnlyForGates(t *testing.T) {
	image := "registry.example/analysis@sha256:" + strings.Repeat("a", 64)
	out, err := exec.Command("helm", "template", "nftest", filepath.Join(repoRoot(t), "deploy/helm/novaforge"),
		"--set-string", "services.gates.analysisImage="+image).CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v: %s", err, out)
	}
	found := 0
	for _, object := range decodeManifests(t, out) {
		if object.Kind != "Deployment" {
			continue
		}
		for _, container := range object.Spec.Template.Spec.Containers {
			for _, env := range container.Env {
				if env.Name == "NF_GATE_ANALYSIS_IMAGE" {
					found++
					if object.Metadata.Name != "nftest-gates" || env.Value != image {
						t.Fatalf("sandbox image miswired to %s: %q", object.Metadata.Name, env.Value)
					}
				}
			}
		}
	}
	if found != 1 {
		t.Fatalf("found %d analysis image bindings, want exactly one", found)
	}
}
