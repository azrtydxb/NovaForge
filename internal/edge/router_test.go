package edge_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/edge"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
)

// stubIdentity implements the generated client so the edge's own authorization
// behaviour can be exercised without standing up the identity service. It is a
// double for a service under separate test, not for a datastore.
type stubIdentity struct {
	identityv1.IdentityServiceClient
	subject *identityv1.Subject
	err     error
}

func (s stubIdentity) ResolveToken(_ context.Context, _ *identityv1.ResolveTokenRequest, _ ...grpc.CallOption) (*identityv1.ResolveTokenResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &identityv1.ResolveTokenResponse{Subject: s.subject}, nil
}

func (s stubIdentity) ResolveSession(_ context.Context, _ *identityv1.ResolveSessionRequest, _ ...grpc.CallOption) (*identityv1.ResolveSessionResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &identityv1.ResolveSessionResponse{Subject: s.subject}, nil
}

func TestUnauthenticatedRejected(t *testing.T) {
	r := edge.NewRouter(edge.Config{Identity: stubIdentity{err: status.Error(codes.Unauthenticated, "no token")}})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/user", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without credentials, got %d", rec.Code)
	}
}

func TestNoCredentialIsNeverTreatedAsUnscoped(t *testing.T) {
	// A request with no credential must be refused outright. Treating it as a
	// caller with an empty scope would make every org-scoped query unfiltered.
	r := edge.NewRouter(edge.Config{Identity: stubIdentity{subject: nil}})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/repos", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestAuthenticatedRequestCarriesOrgScope(t *testing.T) {
	org, user := uuid.New(), uuid.New()
	var gotScope authz.Scope
	var ok bool
	cfg := edge.Config{
		Identity: stubIdentity{subject: &identityv1.Subject{
			UserId: user.String(), OrgId: org.String(), ActorKind: "user",
		}},
		Handlers: map[string]http.HandlerFunc{
			"listRepos": func(w http.ResponseWriter, r *http.Request) {
				s, err := authz.FromContext(r.Context())
				ok = err == nil
				gotScope = s
				edge.WriteJSON(w, http.StatusOK, []string{})
			},
		},
	}
	r := edge.NewRouter(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/repos", nil)
	req.Header.Set("Authorization", "Bearer nf_x")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !ok {
		t.Fatal("handler saw no authz.Scope in the context")
	}
	if gotScope.OrgID != org || gotScope.ActorID != user {
		t.Fatalf("scope not propagated: %+v", gotScope)
	}
}

func TestGRPCStatusMapping(t *testing.T) {
	cases := map[codes.Code]int{
		codes.PermissionDenied: http.StatusForbidden,
		codes.NotFound:         http.StatusNotFound,
		codes.Unauthenticated:  http.StatusUnauthorized,
		codes.AlreadyExists:    http.StatusConflict,
		codes.InvalidArgument:  http.StatusBadRequest,
		codes.Internal:         http.StatusInternalServerError,
	}
	for code, want := range cases {
		if got := edge.StatusFromGRPC(status.Error(code, "x")); got != want {
			t.Errorf("code %v: want HTTP %d, got %d", code, want, got)
		}
	}
}

func TestUnwiredOperationIsLoud(t *testing.T) {
	// A mounted route with no handler must fail loudly rather than 404, so a
	// missing wiring is caught in tests instead of read as "no such resource".
	cfg := edge.Config{Identity: stubIdentity{subject: &identityv1.Subject{
		UserId: uuid.NewString(), OrgId: uuid.NewString(), ActorKind: "user",
	}}}
	r := edge.NewRouter(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/tokens", nil)
	req.Header.Set("Authorization", "Bearer nf_x")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("want 501 for an unwired operation, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no handler wired") {
		t.Fatalf("want an explanatory body, got %s", rec.Body.String())
	}
}
