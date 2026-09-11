package edge_test

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/novaforge/novaforge/internal/edge"
	"gopkg.in/yaml.v3"
)

// TestEveryRouteIsInOpenAPI fails the build when the router and the published
// contract drift apart in either direction. The GUI is deferred, so the
// contract is the only description of the API that clients have.
func TestEveryRouteIsInOpenAPI(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `yaml:"operationId"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse openapi: %v", err)
	}

	documented := map[string]bool{}
	for p, methods := range doc.Paths {
		for m := range methods {
			documented[strings.ToUpper(m)+" "+p] = true
		}
	}

	mounted := map[string]bool{}
	for _, r := range edge.Routes() {
		if r.Pattern == "/healthz" {
			continue // operational, not part of the public contract
		}
		mounted[r.Method+" "+openAPIPath(r.Pattern)] = true
	}

	for k := range mounted {
		if !documented[k] {
			t.Errorf("route %s is mounted but absent from api/openapi.yaml", k)
		}
	}
	for k := range documented {
		if !mounted[k] {
			t.Errorf("api/openapi.yaml documents %s but no route is mounted", k)
		}
	}
}

// TestRouterMountsEveryRoute proves the table is actually wired, not just declared.
func TestRouterMountsEveryRoute(t *testing.T) {
	r := edge.NewRouter(edge.Config{})
	seen := map[string]bool{}
	err := chi.Walk(r.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		seen[method+" "+strings.TrimSuffix(route, "/")] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, rt := range edge.Routes() {
		k := rt.Method + " " + strings.TrimSuffix(rt.Pattern, "/")
		if !seen[k] {
			t.Errorf("route %s declared but not mounted on the router", k)
		}
	}
}

// openAPIPath converts a chi wildcard into the OpenAPI spelling.
func openAPIPath(p string) string {
	return strings.ReplaceAll(p, "/*", "/{path}")
}
