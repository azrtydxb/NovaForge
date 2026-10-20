package service_test

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// renderChart renders the chart with --set pairs and returns its objects as
// generic maps, or the render error.
func renderOIDCChart(t *testing.T, sets ...string) ([]map[string]any, error) {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed; the chart cannot be rendered")
	}
	args := []string{"template", "nftest", filepath.Join(repoRoot(t), "deploy", "helm", "novaforge")}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	var stderr bytes.Buffer
	cmd := exec.Command(helm, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New(stderr.String())
	}
	dec := yaml.NewDecoder(bytes.NewReader(out))
	var objs []map[string]any
	for {
		var m map[string]any
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode render: %v", err)
		}
		if m != nil {
			objs = append(objs, m)
		}
	}
	return objs, nil
}

func oidcObject(objs []map[string]any, kind, name string) map[string]any {
	for _, o := range objs {
		if md, _ := o["metadata"].(map[string]any); o["kind"] == kind && md["name"] == name {
			return o
		}
	}
	return nil
}

func oidcDig(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			l, _ := v.([]any)
			if k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return v
}

func oidcContainerEnv(t *testing.T, objs []map[string]any, deployment string) map[string]map[string]any {
	t.Helper()
	d := oidcObject(objs, "Deployment", deployment)
	if d == nil {
		t.Fatalf("no Deployment %s", deployment)
	}
	env := map[string]map[string]any{}
	list, _ := oidcDig(d, "spec", "template", "spec", "containers", 0, "env").([]any)
	for _, e := range list {
		m, _ := e.(map[string]any)
		name, _ := m["name"].(string)
		env[name] = m
	}
	return env
}

// TestOIDCSecretReachesOnlyTheEdge: the provider settings the platform writes
// into the novaforge-oidc Secret reach the edge — by reference, never as bytes
// in the chart — and nothing else; the password switch reaches both services
// that act on it, from one value.
func TestOIDCSecretReachesOnlyTheEdge(t *testing.T) {
	objs, err := renderOIDCChart(t, "oidc.existingSecret=novaforge-oidc", "oidc.issuerHost=login.azrty.com", "oidc.passwordLoginEnabled=false")
	if err != nil {
		t.Fatalf("render with SSO: %v", err)
	}
	edge := oidcContainerEnv(t, objs, "nftest-edge")
	for _, key := range []string{"OIDC_ISSUER", "OIDC_CLIENT_ID", "OIDC_REDIRECT_URI"} {
		ref := oidcDig(edge[key], "valueFrom", "secretKeyRef")
		if oidcDig(ref, "name") != "novaforge-oidc" || oidcDig(ref, "key") != key {
			t.Errorf("edge %s = %v, want a reference to novaforge-oidc/%s", key, edge[key], key)
		}
	}
	for _, svc := range []string{"edge", "identity"} {
		if v := oidcDig(oidcContainerEnv(t, objs, "nftest-"+svc)["NF_PASSWORD_LOGIN_ENABLED"], "value"); v != "false" {
			t.Errorf("%s NF_PASSWORD_LOGIN_ENABLED = %v, want \"false\"", svc, v)
		}
	}
	for _, svc := range []string{"identity", "git-platform", "agent-runtime"} {
		if _, ok := oidcContainerEnv(t, objs, "nftest-"+svc)["OIDC_ISSUER"]; ok {
			t.Errorf("%s is given OIDC_ISSUER; only the edge talks to the provider", svc)
		}
	}

	// The edge is air-gapped; with SSO it may reach the issuer and nothing
	// else outside the cluster.
	pol := oidcObject(objs, "CiliumNetworkPolicy", "nftest-edge-egress")
	if pol == nil {
		t.Fatal("no edge egress policy")
	}
	var fqdns []string
	egress, _ := oidcDig(pol, "spec", "egress").([]any)
	for _, rule := range egress {
		names, _ := oidcDig(rule, "toFQDNs").([]any)
		for _, n := range names {
			fqdns = append(fqdns, oidcDig(n, "matchName").(string))
			if port := oidcDig(rule, "toPorts", 0, "ports", 0, "port"); port != "443" {
				t.Errorf("issuer egress port = %v, want 443", port)
			}
		}
	}
	if len(fqdns) != 1 || fqdns[0] != "login.azrty.com" {
		t.Errorf("edge may reach %v outside the cluster, want exactly login.azrty.com", fqdns)
	}
}

func TestOIDCOffByDefault(t *testing.T) {
	objs, err := renderOIDCChart(t)
	if err != nil {
		t.Fatal(err)
	}
	edge := oidcContainerEnv(t, objs, "nftest-edge")
	for _, key := range []string{"OIDC_ISSUER", "OIDC_CLIENT_ID", "OIDC_REDIRECT_URI"} {
		if e, ok := edge[key]; !ok || oidcDig(e, "value") != "" || oidcDig(e, "valueFrom") != nil {
			t.Errorf("edge %s = %v, want present and empty (SSO off)", key, e)
		}
	}
	if v := oidcDig(edge["NF_PASSWORD_LOGIN_ENABLED"], "value"); v != "true" {
		t.Errorf("password login by default = %v, want \"true\"", v)
	}
	pol := oidcObject(objs, "CiliumNetworkPolicy", "nftest-edge-egress")
	if egress, _ := oidcDig(pol, "spec", "egress").([]any); len(egress) != 1 {
		t.Errorf("edge egress without SSO = %v, want the in-cluster rule only", egress)
	}
}

// The chart refuses configurations that would deploy a login nobody can use.
func TestOIDCChartRefusesUnusableSignIn(t *testing.T) {
	cases := map[string]struct {
		sets []string
		want string
	}{
		"SSO without a reachable issuer": {[]string{"oidc.existingSecret=novaforge-oidc"}, "oidc.issuerHost is required"},
		"no way to sign in":              {[]string{"oidc.passwordLoginEnabled=false"}, "nobody could sign in"},
		"invalid secret name":            {[]string{"oidc.existingSecret=Not_A_Name", "oidc.issuerHost=x"}, "invalid oidc.existingSecret"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := renderOIDCChart(t, c.sets...); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("render %v: %v, want a refusal mentioning %q", c.sets, err, c.want)
			}
		})
	}
}
