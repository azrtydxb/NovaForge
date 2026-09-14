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
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
)

// stubIdentity implements the generated client so the edge's own authorization
// behaviour can be exercised without standing up the identity service. It is a
// double for a service under separate test, not for a datastore.
type stubIdentity struct {
	identityv1.IdentityServiceClient
	subject     *identityv1.Subject
	err         error
	tokenErr    error // when set, ResolveToken fails but ResolveSession may not
	sessionOnly *identityv1.Subject
}

func (s stubIdentity) ResolveToken(_ context.Context, _ *identityv1.ResolveTokenRequest, _ ...grpc.CallOption) (*identityv1.ResolveTokenResponse, error) {
	if s.tokenErr != nil {
		return nil, s.tokenErr
	}
	if s.err != nil {
		return nil, s.err
	}
	return &identityv1.ResolveTokenResponse{Subject: s.subject}, nil
}

func (s stubIdentity) ResolveSession(_ context.Context, _ *identityv1.ResolveSessionRequest, _ ...grpc.CallOption) (*identityv1.ResolveSessionResponse, error) {
	if s.sessionOnly != nil {
		return &identityv1.ResolveSessionResponse{Subject: s.sessionOnly}, nil
	}
	if s.err != nil {
		return nil, s.err
	}
	return &identityv1.ResolveSessionResponse{Subject: s.subject}, nil
}

// TestBearerAcceptsSessionToken pins a defect the cluster end-to-end test
// caught: nf stores the session token from login and sends it as a Bearer
// header, but the edge only tried personal-access-token resolution for Bearer,
// so every authenticated CLI call failed with "invalid, revoked, or expired
// token". A bearer header carries a credential; the edge must resolve it
// against both kinds before refusing it.
func TestBearerAcceptsSessionToken(t *testing.T) {
	org, user := uuid.New(), uuid.New()
	called := false
	cfg := edge.Config{
		Identity: stubIdentity{
			tokenErr:    status.Error(codes.Unauthenticated, "invalid, revoked, or expired token"),
			sessionOnly: &identityv1.Subject{UserId: user.String(), OrgId: org.String(), ActorKind: "user"},
		},
		Handlers: map[string]http.HandlerFunc{
			"listOrgs": func(w http.ResponseWriter, r *http.Request) {
				called = true
				edge.WriteJSON(w, http.StatusOK, []string{})
			},
		},
	}
	r := edge.NewRouter(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs", nil)
	req.Header.Set("Authorization", "Bearer a-session-token")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("a session token presented as a bearer must authenticate; got %d %s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatal("handler never ran")
	}
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
		// A refused merge and a gate proposal that changes nothing are the
		// caller's answer, not a server fault; a 500 made the GUI report
		// them as the platform breaking.
		codes.FailedPrecondition: http.StatusConflict,
		// "This deployment has no X" is how services say a feature is not
		// configured, and 501 is what the GUI reads as unavailable.
		codes.Unimplemented: http.StatusNotImplemented,
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

// TestCredentialIsForwardedDownstream pins the second defect the cluster
// end-to-end test caught: the edge authenticated a request and then called the
// services anonymously, so every service-side authorization check saw no
// caller and refused with "authentication required". The edge is a gateway, not
// a trusted principal — it must carry the caller's credential onward.
func TestCredentialIsForwardedDownstream(t *testing.T) {
	org, user := uuid.New(), uuid.New()
	var seen string
	cfg := edge.Config{
		Identity: stubIdentity{subject: &identityv1.Subject{
			UserId: user.String(), OrgId: org.String(), ActorKind: "user",
		}},
		Handlers: map[string]http.HandlerFunc{
			"listOrgs": func(w http.ResponseWriter, r *http.Request) {
				seen = edge.CredentialFrom(r.Context())
				edge.WriteJSON(w, http.StatusOK, []string{})
			},
		},
	}
	r := edge.NewRouter(cfg)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs", nil)
	req.Header.Set("Authorization", "Bearer nf_the_caller_token")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	if seen != "nf_the_caller_token" {
		t.Fatalf("credential not carried into the request context, got %q", seen)
	}
}

func TestForwardCredentialAttachesMetadata(t *testing.T) {
	ctx := edge.WithCredential(context.Background(), "nf_tok")
	var gotAuth []string
	invoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		gotAuth = md.Get("authorization")
		return nil
	}
	if err := edge.ForwardCredential(ctx, "/x.Y/Z", nil, nil, nil, invoker); err != nil {
		t.Fatalf("ForwardCredential: %v", err)
	}
	if len(gotAuth) != 1 || gotAuth[0] != "Bearer nf_tok" {
		t.Fatalf("want the bearer attached to outbound metadata, got %v", gotAuth)
	}
}
