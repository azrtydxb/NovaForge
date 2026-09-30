package service_test

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"testing"
)

const lbAnnotation = "kube-vip.io/loadbalancerIPs"

func renderChart(t *testing.T, args ...string) []manifest {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed; the chart cannot be rendered")
	}
	chart := filepath.Join(repoRoot(t), "deploy", "helm", "novaforge")
	base := []string{"template", "nftest", chart, "--set", "runner.orgId=00000000-0000-0000-0000-000000000001"}
	cmd := exec.Command(helm, append(base, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, stderr.String())
	}
	return decodeManifests(t, out)
}

// A LoadBalancer service may pin its kube-vip address; left empty, the
// provider chooses. On kw the provider once gave git-platform 192.168.10.130,
// which BuildKit already held, and two kube-vip leaders then each bound it.
//
// proved by: dropping the annotations block from services.tpl fails the
// first case; rendering it unconditionally fails the second.
func TestLoadBalancerServicesCanPinTheirAddress(t *testing.T) {
	pinned := find(renderChart(t, "--set", "services.git-platform.loadBalancerIP=192.168.10.250"),
		"Service", "nftest-git-platform")
	if pinned == nil || pinned.Metadata.Annotations[lbAnnotation] != "192.168.10.250" {
		t.Fatalf("git-platform with loadBalancerIP set: annotations %v", pinned)
	}
	free := find(renderChart(t), "Service", "nftest-git-platform")
	if free == nil {
		t.Fatal("git-platform service not rendered")
	}
	if v, ok := free.Metadata.Annotations[lbAnnotation]; ok {
		t.Fatalf("git-platform without loadBalancerIP must let the provider choose, got %q", v)
	}
}

// The kw release pins git-platform to its own address, and no two services
// the chart renders ask for the same one.
func TestKwPinsDistinctLoadBalancerAddresses(t *testing.T) {
	values := filepath.Join(repoRoot(t), "deploy", "helm", "novaforge", "values-kw.yaml")
	seen := map[string]string{}
	for _, o := range renderChart(t, "-f", values) {
		ip := o.Metadata.Annotations[lbAnnotation]
		if o.Kind != "Service" || ip == "" {
			continue
		}
		if other, dup := seen[ip]; dup {
			t.Errorf("services %s and %s both request %s", other, o.Metadata.Name, ip)
		}
		seen[ip] = o.Metadata.Name
	}
	if seen["192.168.10.141"] != "nftest-git-platform" {
		t.Errorf("kw git-platform address = %v, want 192.168.10.141", seen)
	}
}
