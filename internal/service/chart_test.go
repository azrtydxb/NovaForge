package service_test

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
	"gopkg.in/yaml.v3"
)

// TestHelmChartDeploysEveryServiceWithTheEnvironmentItsBinaryReads renders the
// chart with the real helm binary and holds it against two other sources of
// truth: values.yaml's list of services, and the configuration each service's
// binary actually reads.
//
// Both halves have failed before without any test noticing. The deploy e2e
// checked that every Deployment present was ready, so a chart that omitted a
// service passed. And a variable a binary reads can be missing from its pod —
// the service then starts, reports the feature unconfigured, and looks exactly
// like a deployment that chose not to have it. A binary's reads are found by
// type-checking its package, not by grepping, so a field read under any
// variable name counts.
func TestHelmChartDeploysEveryServiceWithTheEnvironmentItsBinaryReads(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed; the chart cannot be rendered")
	}
	root := repoRoot(t)
	chart := filepath.Join(root, "deploy", "helm", "novaforge")

	var values struct {
		Services   map[string]map[string]any `yaml:"services"`
		Datastores struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"datastores"`
	}
	raw, err := os.ReadFile(filepath.Join(chart, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatalf("parse values.yaml: %v", err)
	}
	if len(values.Services) == 0 {
		t.Fatal("values.yaml lists no services")
	}

	const release = "nftest"
	// A runner is rendered only when it has an organization; one is given so
	// the whole chart renders, including the runner's own template.
	cmd := exec.Command(helm, "template", release, chart, "--set", "runner.orgId=00000000-0000-0000-0000-000000000001")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, stderr.String())
	}
	objects := decodeManifests(t, out)

	secretKeys := map[string]map[string]bool{}
	for _, o := range objects {
		if o.Kind == "Secret" {
			keys := map[string]bool{}
			for k := range o.StringData {
				keys[k] = true
			}
			for k := range o.Data {
				keys[k] = true
			}
			secretKeys[o.Metadata.Name] = keys
		}
	}

	fields := configEnv(t, filepath.Join(root, "internal", "service", "service.go"))

	names := make([]string, 0, len(values.Services))
	for name := range values.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			full := release + "-" + name
			dep := find(objects, "Deployment", full)
			if dep == nil {
				t.Fatalf("values.yaml lists %s but the chart renders no Deployment %s", name, full)
			}
			if find(objects, "Service", full) == nil {
				t.Errorf("values.yaml lists %s but the chart renders no Service %s", name, full)
			}
			containers := dep.Spec.Template.Spec.Containers
			if len(containers) == 0 {
				t.Fatalf("Deployment %s has no container", full)
			}
			c := containers[0]
			if !strings.HasSuffix(strings.Split(c.Image, ":")[0], "/"+name) {
				t.Errorf("Deployment %s runs image %s, not the %s binary", full, c.Image, name)
			}
			env := map[string]bool{}
			for _, e := range c.Env {
				env[e.Name] = true
			}
			for _, ef := range c.EnvFrom {
				if ef.SecretRef != nil {
					keys, ok := secretKeys[ef.SecretRef.Name]
					if !ok {
						t.Errorf("Deployment %s takes its environment from Secret %s, which the chart does not render", full, ef.SecretRef.Name)
					}
					for k := range keys {
						env[k] = true
					}
				}
			}

			cmdDir := filepath.Join(root, "cmd", name)
			if _, err := os.Stat(cmdDir); err != nil {
				t.Fatalf("values.yaml lists %s but there is no cmd/%s binary to deploy", name, name)
			}
			for _, field := range readFields(t, root, "./cmd/"+name) {
				f, ok := fields[field]
				if !ok || f.hasDefault {
					continue
				}
				if !env[f.env] {
					t.Errorf("cmd/%s reads Config.%s, but its Deployment sets no %s", name, field, f.env)
				}
			}
		})
	}

	// The reverse: nothing the chart deploys as a service is missing from
	// values.yaml, or it would be deployed without being checked above.
	for _, o := range objects {
		if o.Kind != "Deployment" {
			continue
		}
		comp := o.Metadata.Labels["app.kubernetes.io/component"]
		if comp == "" {
			continue
		}
		if _, ok := values.Services[comp]; !ok && comp != "runner" {
			t.Errorf("the chart deploys %s, which values.yaml does not list", o.Metadata.Name)
		}
	}
}

type manifest struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name   string            `yaml:"name"`
		Labels map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	StringData map[string]string `yaml:"stringData"`
	Data       map[string]string `yaml:"data"`
	Spec       struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Image string `yaml:"image"`
					Env   []struct {
						Name string `yaml:"name"`
					} `yaml:"env"`
					EnvFrom []struct {
						SecretRef *struct {
							Name string `yaml:"name"`
						} `yaml:"secretRef"`
					} `yaml:"envFrom"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

func decodeManifests(t *testing.T, out []byte) []manifest {
	t.Helper()
	dec := yaml.NewDecoder(bytes.NewReader(out))
	var objects []manifest
	for {
		var m manifest
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode rendered chart: %v", err)
		}
		if m.Kind != "" {
			objects = append(objects, m)
		}
	}
	return objects
}

func find(objects []manifest, kind, name string) *manifest {
	for i := range objects {
		if objects[i].Kind == kind && objects[i].Metadata.Name == name {
			return &objects[i]
		}
	}
	return nil
}

type configVar struct {
	env        string
	hasDefault bool
}

// configEnv reads LoadConfig's composite literal: each field, the variable it
// reads, and whether LoadConfig supplies a non-zero default when it is unset.
func configEnv(t *testing.T, path string) map[string]configVar {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]configVar{}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "LoadConfig" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				return true
			}
			var call *ast.CallExpr
			ast.Inspect(kv.Value, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok && call == nil {
					call = c
				}
				return call == nil
			})
			if call == nil || len(call.Args) < 2 {
				return true
			}
			name, ok := call.Args[0].(*ast.BasicLit)
			if !ok {
				return true
			}
			envName, _ := strconv.Unquote(name.Value)
			def, _ := call.Args[1].(*ast.BasicLit)
			hasDefault := def != nil && def.Value != `""` && def.Value != "0"
			out[key.Name] = configVar{env: envName, hasDefault: hasDefault}
			return true
		})
		return false
	})
	if len(out) == 0 {
		t.Fatal("found no fields in LoadConfig")
	}
	return out
}

// readFields type-checks a binary's package and returns every service.Config
// field it reads and does not assign a value of its own first. A field a
// binary defaults itself (`if cfg.GRPCPort == 0 { cfg.GRPCPort = ... }`) does
// not have to come from the chart.
func readFields(t *testing.T, root, pattern string) []string {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:  root,
	}
	pkgs, err := packages.Load(cfg, pattern)
	if err != nil {
		t.Fatalf("load %s: %v", pattern, err)
	}
	read := map[string]bool{}
	assigned := map[string]bool{}
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			t.Fatalf("load %s: %v", pattern, pkg.Errors[0])
		}
		isConfigField := func(sel *ast.SelectorExpr) (string, bool) {
			s := pkg.TypesInfo.Selections[sel]
			if s == nil || s.Kind() != types.FieldVal {
				return "", false
			}
			named, ok := types.Unalias(derefType(s.Recv())).(*types.Named)
			if !ok || named.Obj().Name() != "Config" || named.Obj().Pkg() == nil ||
				named.Obj().Pkg().Path() != "github.com/novaforge/novaforge/internal/service" {
				return "", false
			}
			return sel.Sel.Name, true
		}
		for _, f := range pkg.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.AssignStmt:
					for _, lhs := range x.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok {
							if name, ok := isConfigField(sel); ok {
								assigned[name] = true
							}
						}
					}
				case *ast.SelectorExpr:
					if name, ok := isConfigField(x); ok {
						read[name] = true
					}
				}
				return true
			})
		}
	}
	var out []string
	for name := range read {
		if !assigned[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func derefType(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		return p.Elem()
	}
	return t
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
