package service_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// okeSecretContract is every Secret values-oke.yaml names and the keys the
// rendered pods need from it. The chart renders none of them — the platform
// repo provides them through SOPS — so this is the only place the chart's
// demands and docs/oke-deployment.md's list are held together: the env test
// below fails when a pod needs a key missing here, and the doc test fails when
// the doc omits one listed here.
var okeSecretContract = map[string][]string{
	"novaforge-secrets": {
		"JWT_SECRET", "HMAC_SECRET", "SECRETS_KEK", "AI_API_KEY",
		"REDIS_URL", "S3_ENDPOINT", "S3_ACCESS_KEY", "S3_SECRET_KEY",
	},
	"novaforge-database": {"uri"},
	"ocir-pull":          {".dockerconfigjson"},
	// Written by the platform's zitadel-config runner, not by SOPS.
	"novaforge-oidc": {"OIDC_ISSUER", "OIDC_CLIENT_ID", "OIDC_REDIRECT_URI"},
}

const okeRelease = "novaforge"

func renderOKE(t *testing.T) []byte {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed; the chart cannot be rendered")
	}
	chart := filepath.Join(repoRoot(t), "deploy", "helm", "novaforge")
	cmd := exec.Command(helm, "template", okeRelease, chart, "-n", "novaforge", "-f", filepath.Join(chart, "values-oke.yaml"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template -f values-oke.yaml: %v\n%s", err, stderr.String())
	}
	return out
}

// The env guard of TestHelmChartDeploysEveryServiceWithTheEnvironmentItsBinaryReads,
// run against the OKE render: every Config field a binary reads reaches its
// pod, with the existing Secrets described by okeSecretContract instead of
// rendered. The external database is exactly the seam this catches — a
// DATABASE_URL taken from a Secret key nobody provides fails here, not as a
// pod stuck in CreateContainerConfigError.
func TestOKEChartDeploysEveryServiceWithTheEnvironmentItsBinaryReads(t *testing.T) {
	out := renderOKE(t)
	root := repoRoot(t)
	var values struct {
		Services map[string]map[string]any `yaml:"services"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "deploy", "helm", "novaforge", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	contract := map[string]map[string]bool{}
	for name, keys := range okeSecretContract {
		contract[name] = map[string]bool{}
		for _, k := range keys {
			contract[name][k] = true
		}
	}
	checkServiceEnvironment(t, root, decodeManifests(t, out), okeRelease, "novaforge-", values.Services, contract)
}

// What OKE refuses or cannot have: anything cluster-scoped or a Secret (the
// namespace-scoped deployer cannot create them, and secrets come from SOPS), a
// Cilium object (flannel; the kind does not exist), a public LoadBalancer
// beside the Gateway, a short image name (CRI-O short-name enforcing), a pod
// without the OCIR pull secret, or a render that differs between two runs
// (Kuvryn Sync renders offline; randAlphaNum or lookup would make every sync a
// change, or render nothing).
func TestOKERenderIsDeployableByANamespaceScopedOfflineRenderer(t *testing.T) {
	out := renderOKE(t)
	if again := renderOKE(t); !bytes.Equal(out, again) {
		t.Fatal("two renders of values-oke.yaml differ: the chart uses a random or cluster-dependent value")
	}
	dec := yaml.NewDecoder(bytes.NewReader(out))
	qualified := regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)+(:[0-9]+)?/`)
	for {
		var o map[string]any
		err := dec.Decode(&o)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if o == nil {
			continue
		}
		kind, _ := o["kind"].(string)
		name, _ := o["metadata"].(map[string]any)["name"].(string)
		switch kind {
		case "Secret", "ServiceAccount", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding",
			"CiliumNetworkPolicy", "Namespace", "Certificate":
			t.Errorf("values-oke renders %s %s, which OKE's deployer must not or cannot create", kind, name)
		case "Service":
			if typ, _ := o["spec"].(map[string]any)["type"].(string); typ == "LoadBalancer" {
				t.Errorf("Service %s is a LoadBalancer; on OKE the Gateway is the only way in", name)
			}
		case "Deployment":
			pod := o["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			for _, c := range pod["containers"].([]any) {
				image, _ := c.(map[string]any)["image"].(string)
				if !qualified.MatchString(image) {
					t.Errorf("Deployment %s runs %q, which CRI-O's short-name mode refuses", name, image)
				}
				if strings.HasPrefix(image, "dxb.ocir.io/") {
					secrets, _ := pod["imagePullSecrets"].([]any)
					if len(secrets) != 1 || secrets[0].(map[string]any)["name"] != "ocir-pull" {
						t.Errorf("Deployment %s pulls from OCIR without imagePullSecrets ocir-pull: %v", name, secrets)
					}
				}
			}
		}
	}
}

// Every feature that runs repository code is confined only by a
// NetworkPolicy, which flannel does not enforce, so values-oke keeps each one
// in the state where the platform refuses it. A value flipped back on here is
// untrusted code with a route to shared-pg and the internet.
func TestOKEKeepsCodeExecutionOffWhileNetworkPolicyIsUnenforced(t *testing.T) {
	var values struct {
		NetworkPolicy struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"networkPolicy"`
		Runner struct {
			OrgID string `yaml:"orgId"`
		} `yaml:"runner"`
		OperatorConfigs map[string]struct {
			SecretName *string `yaml:"secretName"`
		} `yaml:"operatorConfigs"`
		Services map[string]map[string]any `yaml:"services"`
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "helm", "novaforge", "values-oke.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	if values.NetworkPolicy.Enabled {
		t.Error("networkPolicy.enabled renders CiliumNetworkPolicy objects, a kind OKE does not have")
	}
	if values.Runner.OrgID != "" {
		t.Error("runner.orgId deploys a CI runner, which runs repository commands in unconfined pods")
	}
	for _, key := range []string{"semanticProducer", "deployments"} {
		cfg, ok := values.OperatorConfigs[key]
		if !ok || cfg.SecretName == nil || *cfg.SecretName != "" {
			t.Errorf("operatorConfigs.%s must be explicitly empty on OKE", key)
		}
	}
	gates := values.Services["gates"]
	if image, ok := gates["analysisImage"]; !ok || image != "" {
		t.Error("services.gates.analysisImage must be explicitly empty: executable gates run repository code")
	}
	runtime := values.Services["agent-runtime"]
	if runtime["automountServiceAccountToken"] != false {
		t.Error("services.agent-runtime.automountServiceAccountToken must be false: with a token agent-runtime provisions workspaces that run code")
	}

	// And the render agrees: the agent-runtime pod has no token.
	dec := yaml.NewDecoder(bytes.NewReader(renderOKE(t)))
	for {
		var o struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				Template struct {
					Spec struct {
						Automount *bool `yaml:"automountServiceAccountToken"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		err := dec.Decode(&o)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if o.Kind == "Deployment" && o.Metadata.Name == okeRelease+"-agent-runtime" {
			if a := o.Spec.Template.Spec.Automount; a == nil || *a {
				t.Error("the OKE render mounts a service account token into agent-runtime")
			}
			return
		}
	}
	t.Fatal("no agent-runtime Deployment rendered")
}

type httpRoute struct {
	Kind string `yaml:"kind"`
	Spec struct {
		ParentRefs []struct {
			Name        string `yaml:"name"`
			Namespace   string `yaml:"namespace"`
			SectionName string `yaml:"sectionName"`
		} `yaml:"parentRefs"`
		Hostnames []string `yaml:"hostnames"`
		Rules     []struct {
			Matches []struct {
				Path struct {
					Type  string `yaml:"type"`
					Value string `yaml:"value"`
				} `yaml:"path"`
			} `yaml:"matches"`
			BackendRefs []struct {
				Name string `yaml:"name"`
				Port int    `yaml:"port"`
			} `yaml:"backendRefs"`
		} `yaml:"rules"`
	} `yaml:"spec"`
}

// Git and the application share novaforge.azrty.com. The git rule must come
// first, send exactly git-platform's paths to its plaintext port, and leave
// every page of the application to edge — a regex too wide captures the GUI,
// one too narrow makes a clone 404 at edge. The expression is compiled with
// Go's regexp, which is RE2 like Envoy's safe_regex, and matched whole as
// Envoy matches it.
//
// proved by: swapping the two rules in httproute.tpl fails the order check;
// dropping "info/lfs/objects/batch" from gitPathRegex fails the LFS case.
func TestOKEHTTPRouteSendsGitToGitPlatformBeforeTheCatchAll(t *testing.T) {
	var route *httpRoute
	dec := yaml.NewDecoder(bytes.NewReader(renderOKE(t)))
	for {
		var r httpRoute
		err := dec.Decode(&r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if r.Kind == "HTTPRoute" {
			if route != nil {
				t.Fatal("values-oke renders more than one HTTPRoute")
			}
			route = &r
		}
	}
	if route == nil {
		t.Fatal("values-oke renders no HTTPRoute")
	}
	if len(route.Spec.ParentRefs) != 1 || route.Spec.ParentRefs[0].Name != "edge" ||
		route.Spec.ParentRefs[0].Namespace != "envoy-gateway-system" || route.Spec.ParentRefs[0].SectionName != "novaforge-https" {
		t.Errorf("parentRefs = %+v, want edge/envoy-gateway-system section novaforge-https", route.Spec.ParentRefs)
	}
	if strings.Join(route.Spec.Hostnames, ",") != "novaforge.azrty.com" {
		t.Errorf("hostnames = %v", route.Spec.Hostnames)
	}
	if len(route.Spec.Rules) != 2 {
		t.Fatalf("want exactly a git rule and a catch-all, got %d rules", len(route.Spec.Rules))
	}
	git, app := route.Spec.Rules[0], route.Spec.Rules[1]
	if len(git.Matches) != 1 || git.Matches[0].Path.Type != "RegularExpression" {
		t.Fatalf("first rule is not the git RegularExpression rule: %+v", git.Matches)
	}
	if len(git.BackendRefs) != 1 || git.BackendRefs[0].Name != okeRelease+"-git-platform" || git.BackendRefs[0].Port != 8081 {
		t.Errorf("git rule backends = %+v, want git-platform:8081", git.BackendRefs)
	}
	if len(app.Matches) != 1 || app.Matches[0].Path.Type != "PathPrefix" || app.Matches[0].Path.Value != "/" {
		t.Errorf("second rule is not the / catch-all: %+v", app.Matches)
	}
	if len(app.BackendRefs) != 1 || app.BackendRefs[0].Name != okeRelease+"-edge" || app.BackendRefs[0].Port != 8080 {
		t.Errorf("catch-all backends = %+v, want edge:8080", app.BackendRefs)
	}

	re, err := regexp.Compile(git.Matches[0].Path.Value)
	if err != nil {
		t.Fatalf("git path expression is not RE2: %v", err)
	}
	full := func(p string) bool { m := re.FindString(p); return m == p }
	oid := strings.Repeat("ab", 32)
	for _, p := range []string{
		"/acme/app.git/info/refs",
		"/acme/app.git/git-upload-pack",
		"/acme/app.git/git-receive-pack",
		"/acme/app.git/info/lfs/objects/batch",
		"/acme/app.git/info/lfs/objects/" + oid,
		"/0b9e1d0a-1111-2222-3333-444455556666/my.repo.git/info/refs",
	} {
		if !full(p) {
			t.Errorf("git path %s would go to edge", p)
		}
	}
	for _, p := range []string{
		"/", "/api/v1/orgs", "/assets/index.js", "/acme/app", "/acme/app.git",
		"/acme/app.git/HEAD", "/acme/app.git/info/refs/extra", "/a/b/c.git/info/refs",
		"/acme/app.github/info/refs",
	} {
		if full(p) {
			t.Errorf("non-git path %s would go to git-platform", p)
		}
	}
}

// The origin git-platform advertises to git-lfs is the route's host over
// https: anything else sends LFS transfers to an address the Gateway does not
// serve, or to plaintext.
func TestOKEAdvertisesTheGatewayHostForGitAndLFS(t *testing.T) {
	for _, o := range decodeManifests(t, renderOKE(t)) {
		if o.Kind != "Deployment" || o.Metadata.Name != okeRelease+"-git-platform" {
			continue
		}
		for _, e := range o.Spec.Template.Spec.Containers[0].Env {
			if e.Name == "NF_GIT_PUBLIC_URL" {
				if e.Value != "https://novaforge.azrty.com" {
					t.Errorf("NF_GIT_PUBLIC_URL = %q", e.Value)
				}
				return
			}
		}
		t.Fatal("git-platform has no NF_GIT_PUBLIC_URL")
	}
	t.Fatal("no git-platform Deployment rendered")
}

// docs/oke-deployment.md is what the platform repo's SOPS secrets are written
// from. Every Secret and key the chart needs is named in it.
func TestOKEDocumentListsEverySecretTheChartNeeds(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "oke-deployment.md"))
	if err != nil {
		t.Fatal(err)
	}
	for name, keys := range okeSecretContract {
		if !bytes.Contains(doc, []byte("`"+name+"`")) {
			t.Errorf("docs/oke-deployment.md does not name Secret %s", name)
		}
		for _, k := range keys {
			if !bytes.Contains(doc, []byte("`"+k+"`")) {
				t.Errorf("docs/oke-deployment.md does not list key %s of %s", k, name)
			}
		}
	}
}
