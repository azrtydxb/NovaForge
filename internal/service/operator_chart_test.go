package service_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Operator credentials belong only in their consuming service, not the shared
// envFrom secret. Mount directories (not subPath files) so rotation is visible.
func TestOperatorConfigMountsAreOwnerScoped(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal("helm required for operator mount acceptance")
	}
	bindings := []struct{ key, owner, env string }{
		{"openbao", "gates", "NF_OPENBAO_CONFIG_FILE"},
		{"deployments", "gates", "NF_DEPLOYMENT_CONFIG_FILE"},
		{"semanticProducer", "engineering-graph", "NF_SEMANTIC_PRODUCER_CONFIG_FILE"},
		{"mcpHTTP", "agent-runtime", "NF_MCP_HTTP_CONFIG_FILE"},
		{"reviews", "work-reviews", "NF_REVIEW_CONFIG_FILE"},
	}
	var values strings.Builder
	values.WriteString("operatorConfigs:\n")
	for _, b := range bindings {
		values.WriteString("  " + b.key + ":\n    secretName: owned-" + strings.ToLower(b.key) + "\n    configKey: settings.json\n")
	}
	file := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(file, []byte(values.String()), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(helm, "template", "nftest", filepath.Join(repoRoot(t), "deploy/helm/novaforge"), "-f", file).CombinedOutput()
	if err != nil {
		t.Fatalf("render: %v: %s", err, out)
	}
	type mount struct {
		Name     string `yaml:"name"`
		Path     string `yaml:"mountPath"`
		ReadOnly bool   `yaml:"readOnly"`
		SubPath  string `yaml:"subPath"`
	}
	type deployment struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					SecurityContext struct {
						FSGroup int64 `yaml:"fsGroup"`
					} `yaml:"securityContext"`
					Containers []struct {
						Env    []struct{ Name, Value string } `yaml:"env"`
						Mounts []mount                        `yaml:"volumeMounts"`
					} `yaml:"containers"`
					Volumes []struct {
						Name   string `yaml:"name"`
						Secret *struct {
							Name string `yaml:"secretName"`
						} `yaml:"secret"`
					} `yaml:"volumes"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	var deployments []deployment
	dec := yaml.NewDecoder(bytes.NewReader(out))
	for {
		var obj deployment
		err := dec.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if obj.Kind == "Deployment" {
			deployments = append(deployments, obj)
		}
	}
	for _, b := range bindings {
		t.Run(b.key, func(t *testing.T) {
			owners := 0
			for _, d := range deployments {
				owner := d.Metadata.Name == "nftest-"+b.owner
				if owner {
					owners++
					if d.Spec.Template.Spec.SecurityContext.FSGroup != 65532 {
						t.Error("nonroot consumer cannot read group-only operator secret")
					}
				}
				foundEnv, foundMount, foundVolume := false, false, false
				volumeName := "operator-" + strings.ToLower(b.key)
				directory := "/etc/novaforge/operator/" + b.key
				for _, c := range d.Spec.Template.Spec.Containers {
					for _, e := range c.Env {
						if e.Name == b.env && e.Value != "" {
							foundEnv = true
							if e.Value != directory+"/settings.json" {
								t.Errorf("wrong config path %q", e.Value)
							}
						}
					}
					for _, m := range c.Mounts {
						if m.Name == volumeName {
							foundMount = true
							if m.Path != directory || !m.ReadOnly || m.SubPath != "" {
								t.Errorf("unsafe mount: %+v", m)
							}
						}
					}
				}
				for _, v := range d.Spec.Template.Spec.Volumes {
					if v.Secret != nil && v.Secret.Name == "owned-"+strings.ToLower(b.key) {
						foundVolume = true
					}
				}
				if foundEnv != owner || foundMount != owner || foundVolume != owner {
					t.Errorf("%s owner=%v env=%v mount=%v volume=%v", d.Metadata.Name, owner, foundEnv, foundMount, foundVolume)
				}
			}
			if owners != 1 {
				t.Fatalf("want exactly one owner, got %d", owners)
			}
		})
	}
}
