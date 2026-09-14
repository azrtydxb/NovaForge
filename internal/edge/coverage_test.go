package edge_test

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	mcpv1 "github.com/novaforge/novaforge/gen/novaforge/mcp/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/edge"
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

// TestEveryRouteHasAHandlerWhenWired pins the seam the other two tests leave
// open: a route can be in the table, in the contract, and mounted, and still
// have no handler behind it — the router then answers 501, or 404 where the
// route was never added at all. That is what happened to the decompose
// route, which existed everywhere except in the router, and presented as a
// bare 404 to anyone who called it.
//
// Every client is non-nil here (they are only dialled when used), so this
// asserts the fully-wired deployment: every operation the table declares
// must have a handler.
func TestEveryRouteHasAHandlerWhenWired(t *testing.T) {
	cfg := edge.Config{
		Identity: identityv1.NewIdentityServiceClient(nil),
		Git:      gitv1.NewGitServiceClient(nil),
		Work:     workv1.NewWorkServiceClient(nil),
		Reviews:  reviewsv1.NewReviewsServiceClient(nil),
		CI:       civ1.NewCIServiceClient(nil),
		Agents:   agentsv1.NewAgentServiceClient(nil),
		Graph:    graphv1.NewGraphServiceClient(nil),
		Gates:    gatesv1.NewGatesServiceClient(nil),
		MCP:      mcpv1.NewMcpServiceClient(nil),
	}
	handlers := edge.Handlers(cfg)

	for _, r := range edge.Routes() {
		if _, ok := handlers[r.OpID]; !ok {
			t.Errorf("route %s %s declares operation %q, but no handler is built for it",
				r.Method, r.Pattern, r.OpID)
		}
	}
	for op := range handlers {
		var declared bool
		for _, r := range edge.Routes() {
			if r.OpID == op {
				declared = true
				break
			}
		}
		if !declared {
			t.Errorf("handler %q is built but no route declares it, so nothing can reach it", op)
		}
	}
}
