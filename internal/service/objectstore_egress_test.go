package service_test

import (
	"fmt"
	"testing"
)

// externalEgress lists what an egress policy lets its pods reach outside the
// cluster, as "cidr:port" or "fqdn:port".
func externalEgress(pol map[string]any) []string {
	var out []string
	egress, _ := oidcDig(pol, "spec", "egress").([]any)
	for _, rule := range egress {
		port := oidcDig(rule, "toPorts", 0, "ports", 0, "port")
		cidrs, _ := oidcDig(rule, "toCIDR").([]any)
		for _, c := range cidrs {
			out = append(out, fmt.Sprintf("%v:%v", c, port))
		}
		names, _ := oidcDig(rule, "toFQDNs").([]any)
		for _, n := range names {
			out = append(out, fmt.Sprintf("%v:%v", oidcDig(n, "matchName"), port))
		}
	}
	return out
}

// TestObjectStoreEgressReachesOnlyItsUsers pins the egress an object store
// outside the cluster needs. git-platform and ci-runner are air-gapped: their
// policy allows the cluster and nothing else, so pointing S3_ENDPOINT at the
// TrueNAS S3 service (kw) left both refused at start, exactly as when the
// bucket is missing. They, and only they, may reach that one address and port.
func TestObjectStoreEgressReachesOnlyItsUsers(t *testing.T) {
	objs, err := renderOIDCChart(t, "datastores.minio.enabled=false",
		"objectStorage.egress[0].host=192.168.10.253", "objectStorage.egress[0].port=9000")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	users := map[string]bool{"git-platform": true, "ci-runner": true}
	for _, svc := range []string{"identity", "git-platform", "ci-runner", "agent-runtime", "engineering-graph", "mcp-server", "edge"} {
		pol := oidcObject(objs, "CiliumNetworkPolicy", "nftest-"+svc+"-egress")
		if pol == nil {
			t.Fatalf("no %s egress policy", svc)
		}
		got := externalEgress(pol)
		if users[svc] {
			if len(got) != 1 || got[0] != "192.168.10.253/32:9000" {
				t.Errorf("%s may reach %v outside the cluster, want exactly 192.168.10.253/32:9000", svc, got)
			}
		} else if len(got) != 0 {
			t.Errorf("%s does not use object storage but may reach %v", svc, got)
		}
	}
}
