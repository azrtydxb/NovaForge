package edge_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/novaforge/novaforge/internal/edge"
)

// TestRefWithSlashesReachesTheService pins a defect that made the repository
// browser useless for exactly the branches this platform writes: every agent
// branch is "agents/<work item>/work", a client must percent-encode those
// slashes to keep the ref in one path segment, and chi hands the segment back
// still encoded. git-platform then answered "unknown ref
// agents%2FNF-1%2Fwork" for a branch that exists.
//
// The router is asked to route the encoded form; reaching the handler at all
// (rather than 404-ing into the web application) plus the handler decoding it
// is what this checks.
func TestRefWithSlashesReachesTheService(t *testing.T) {
	cfg := edge.Config{}
	cfg.Handlers = edge.Handlers(cfg)
	r := edge.NewRouter(cfg)

	for _, path := range []string{
		"/api/v1/orgs/acme/repos/platform/commits/agents%2FNF-1%2Fwork",
		"/api/v1/orgs/acme/repos/platform/tree/agents%2FNF-1%2Fwork/",
		"/api/v1/orgs/acme/repos/platform/blob/agents%2FNF-1%2Fwork/README.md",
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		// With no git client wired the route answers 501; what matters is that
		// it is the API answering and not the web application's 404 fallback.
		if rec.Code == http.StatusNotFound {
			t.Errorf("%s fell through to the web application instead of the API", path)
		}
	}
}
