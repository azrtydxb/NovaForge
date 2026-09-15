package service_test

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestAirGappedAgentRun is spec S-9's criterion at the chart: every service that
// calls the model gateway, or has no business outside the cluster, renders with
// an egress policy that admits only the cluster and the Kubernetes API, and the
// model and embedding endpoints are in-cluster addresses. Nothing checked
// either: an endpoint pointed at a hosted provider would have been followed,
// and no policy would have stopped the packet.
//
// gates and work-reviews are deliberately outside the policy — their scanners
// fetch vulnerability data and module metadata — and the test pins that list
// too, so widening it is a visible decision.
func TestAirGappedAgentRun(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed; the chart cannot be rendered")
	}
	chart := filepath.Join(repoRoot(t), "deploy", "helm", "novaforge")

	var values struct {
		Services      map[string]any `yaml:"services"`
		NetworkPolicy struct {
			Enabled   bool     `yaml:"enabled"`
			AirGapped []string `yaml:"airGapped"`
		} `yaml:"networkPolicy"`
		AI struct {
			Endpoint      string `yaml:"endpoint"`
			EmbedEndpoint string `yaml:"embedEndpoint"`
		} `yaml:"ai"`
	}
	raw, err := os.ReadFile(filepath.Join(chart, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}

	for _, endpoint := range []string{values.AI.Endpoint, values.AI.EmbedEndpoint} {
		u, err := url.Parse(endpoint)
		if err != nil || !strings.HasSuffix(u.Hostname(), ".svc.cluster.local") {
			t.Errorf("model endpoint %q is not an in-cluster service address", endpoint)
		}
	}

	if !values.NetworkPolicy.Enabled {
		t.Fatal("networkPolicy.enabled is false: nothing keeps model traffic in the cluster")
	}
	var exempt []string
	listed := map[string]bool{}
	for _, s := range values.NetworkPolicy.AirGapped {
		listed[s] = true
	}
	for name := range values.Services {
		if !listed[name] {
			exempt = append(exempt, name)
		}
	}
	sort.Strings(exempt)
	if strings.Join(exempt, ",") != "gates,work-reviews" {
		t.Errorf("services outside the egress policy = %v, want exactly gates and work-reviews", exempt)
	}
	if !listed["agent-runtime"] || !listed["engineering-graph"] {
		t.Fatal("the services that call the model are not air-gapped")
	}

	const release = "nfairgap"
	cmd := exec.Command(helm, "template", release, chart)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, stderr.String())
	}

	type policy struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec struct {
			EndpointSelector struct {
				MatchLabels map[string]string `yaml:"matchLabels"`
			} `yaml:"endpointSelector"`
			Egress []map[string]any `yaml:"egress"`
		} `yaml:"spec"`
	}
	policies := map[string]policy{}
	dec := yaml.NewDecoder(bytes.NewReader(out))
	for {
		var p policy
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode rendered chart: %v", err)
		}
		if p.Kind == "CiliumNetworkPolicy" {
			policies[p.Spec.EndpointSelector.MatchLabels["app.kubernetes.io/component"]] = p
		}
	}

	for _, name := range values.NetworkPolicy.AirGapped {
		p, ok := policies[name]
		if !ok {
			t.Errorf("%s has no egress policy", name)
			continue
		}
		if p.Spec.EndpointSelector.MatchLabels["app.kubernetes.io/instance"] != release {
			t.Errorf("%s's policy selects another release's pods: %v", name, p.Spec.EndpointSelector.MatchLabels)
		}
		if len(p.Spec.Egress) == 0 {
			t.Errorf("%s's policy has no egress rule, which Cilium reads as no restriction", name)
		}
		for _, rule := range p.Spec.Egress {
			for key, val := range rule {
				if key != "toEntities" {
					t.Errorf("%s's egress admits %s %v; only entities are allowed", name, key, val)
					continue
				}
				for _, e := range val.([]any) {
					if e != "cluster" && e != "kube-apiserver" {
						t.Errorf("%s's egress admits the %v entity, which is outside the cluster", name, e)
					}
				}
			}
		}
	}
}
