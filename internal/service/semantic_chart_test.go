package service_test

import (
	"bytes"
	"gopkg.in/yaml.v3"
	"io"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSemanticProducerHasDedicatedControllerAuthority(t *testing.T) {
	out, err := exec.Command("helm", "template", "nftest", filepath.Join(repoRoot(t), "deploy/helm/novaforge"), "--set", "operatorConfigs.semanticProducer.secretName=producer-policy").CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v %s", err, out)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(out))
	role, account, binding := false, false, false
	for {
		var object struct {
			Kind     string
			Metadata struct{ Name string }
			Rules    []struct{ Resources, Verbs []string }
			Spec     struct {
				Template struct {
					Spec struct {
						ServiceAccountName string `yaml:"serviceAccountName"`
					}
				}
			}
		}
		if err := decoder.Decode(&object); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if object.Metadata.Name != "nftest-engineering-graph" {
			continue
		}
		switch object.Kind {
		case "Deployment":
			account = object.Spec.Template.Spec.ServiceAccountName == "nftest-engineering-graph"
		case "ClusterRoleBinding":
			binding = true
		case "ClusterRole":
			role = true
			resources := map[string]bool{}
			for _, rule := range object.Rules {
				for _, r := range rule.Resources {
					resources[r] = true
					switch r {
					case "namespaces", "pods", "pods/exec", "networkpolicies":
					default:
						t.Fatalf("unnecessary producer authority %s", r)
					}
				}
			}
			for _, r := range []string{"namespaces", "pods", "pods/exec", "networkpolicies"} {
				if !resources[r] {
					t.Errorf("missing producer authority %s", r)
				}
			}
		}
	}
	if !role || !account || !binding {
		t.Fatalf("configured producer lacks controller wiring: role=%v account=%v binding=%v", role, account, binding)
	}
}
