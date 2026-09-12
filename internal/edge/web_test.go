package edge_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/novaforge/novaforge/internal/edge"
)

// TestUnknownPathReachesTheWebApp pins that a deep link into the application
// is answered by the application rather than by a 404. The app routes
// client-side, so /runs/platform/7 never exists as a file on the server.
func TestUnknownPathReachesTheWebApp(t *testing.T) {
	rec := serve(t, "/runs/platform/7")

	// Without a built application the web handler says so in its body; with
	// one it serves index.html. Either proves the request reached the web
	// handler rather than falling through as a bare 404. The body is what
	// distinguishes it from the router's own "no handler wired" 501.
	if rec.Code == http.StatusOK {
		return
	}
	if !strings.Contains(rec.Body.String(), "without the web application") {
		t.Fatalf("a client-side route answered %d (%q); it never reached the web handler",
			rec.Code, rec.Body.String())
	}
}

// TestAPIPathsAreNotShadowedByTheWebApp pins the other direction: mounting
// the application as the not-found handler must not swallow an API route.
func TestAPIPathsAreNotShadowedByTheWebApp(t *testing.T) {
	rec := serve(t, "/healthz")

	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz answered %d; the web handler shadowed an API route", rec.Code)
	}
}

// serve builds a router with its handlers wired — an empty Config answers 501
// to every route because none are built, which would make either assertion
// above pass for the wrong reason.
func serve(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	cfg := edge.Config{}
	cfg.Handlers = edge.Handlers(cfg)
	rec := httptest.NewRecorder()
	edge.NewRouter(cfg).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}
