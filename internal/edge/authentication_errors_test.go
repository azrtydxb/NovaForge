package edge_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/edge"
	"github.com/novaforge/novaforge/internal/platformtest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAuthenticationPreservesIdentityFailures(t *testing.T) {
	for _, tc := range []struct {
		name           string
		token, session codes.Code
		want           int
	}{
		{"unavailable", codes.Unavailable, codes.Unavailable, 503},
		{"deadline", codes.DeadlineExceeded, codes.DeadlineExceeded, 504},
		{"token outage session invalid", codes.Unavailable, codes.Unauthenticated, 503},
		{"token invalid session outage", codes.Unauthenticated, codes.Unavailable, 503},
		{"invalid", codes.Unauthenticated, codes.Unauthenticated, 401},
		{"forbidden", codes.PermissionDenied, codes.PermissionDenied, 403},
	} {
		for _, credential := range []string{"bearer", "cookie"} {
			t.Run(tc.name+"/"+credential, func(t *testing.T) {
				router := edge.NewRouter(edge.Config{Identity: stubIdentity{tokenErr: status.Error(tc.token, "token resolution"), err: status.Error(tc.session, "session resolution")}})
				req := httptest.NewRequest(http.MethodGet, "/api/v1/user", nil)
				if credential == "bearer" {
					req.Header.Set("Authorization", "Bearer credential")
				} else {
					req.AddCookie(&http.Cookie{Name: "nf_session", Value: "credential"})
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, req)
				want := tc.want
				if credential == "cookie" {
					switch tc.session {
					case codes.Unauthenticated:
						want = 401
					case codes.Unavailable:
						want = 503
					case codes.DeadlineExceeded:
						want = 504
					case codes.PermissionDenied:
						want = 403
					}
				}
				if response.Code != want {
					t.Fatalf("identity %s: HTTP %d, want %d", tc.name, response.Code, want)
				}
			})
		}
	}
}

func TestAuthenticationMissingIdentityIsNotInvalidCredential(t *testing.T) {
	for _, tc := range []struct {
		token string
		want  int
	}{{"", 401}, {"credential", 503}} {
		req := httptest.NewRequest("GET", "/api/v1/user", nil)
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		rec := httptest.NewRecorder()
		edge.NewRouter(edge.Config{}).ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("missing identity: %d want %d", rec.Code, tc.want)
		}
	}
}

// A failed credential kind cannot erase stronger evidence from the other:
// success wins, then upstream failure, then authorization denial, then invalid.
func TestAuthenticationFallbackResultMatrix(t *testing.T) {
	sessions := []codes.Code{codes.OK, codes.Unauthenticated, codes.PermissionDenied, codes.Unavailable, codes.DeadlineExceeded}
	for _, row := range []struct {
		token codes.Code
		want  []int
	}{
		{codes.Unauthenticated, []int{200, 401, 403, 503, 504}},
		{codes.PermissionDenied, []int{200, 403, 403, 503, 504}},
		{codes.Unavailable, []int{200, 503, 503, 503, 503}},
		{codes.DeadlineExceeded, []int{200, 504, 504, 504, 504}},
	} {
		for i, session := range sessions {
			t.Run(row.token.String()+"/"+session.String(), func(t *testing.T) {
				identity := stubIdentity{tokenErr: status.Error(row.token, "private token resolution detail"), err: status.Error(session, "private session resolution detail")}
				if session == codes.OK {
					identity.sessionOnly = &identityv1.Subject{UserId: uuid.NewString(), OrgId: uuid.NewString(), ActorKind: "user"}
				}
				called := false
				router := edge.NewRouter(edge.Config{Identity: identity, Handlers: map[string]http.HandlerFunc{"listRepos": func(w http.ResponseWriter, r *http.Request) {
					called = true
					edge.WriteJSON(w, 200, map[string]any{"repos": []any{}})
				}}})
				req := httptest.NewRequest("GET", "/api/v1/orgs/fixture/repos", nil)
				req.Header.Set("Authorization", "Bearer fixture-credential")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, req)
				if response.Code != row.want[i] || called != (row.want[i] == 200) {
					t.Fatalf("token=%v session=%v: HTTP%d want%d handler=%v", row.token, session, response.Code, row.want[i], called)
				}
				if response.Code == 403 && strings.Contains(response.Body.String(), "private") {
					t.Fatal("authorization response exposed upstream resolution details")
				}
			})
		}
	}
}

func TestAuthenticationValidNonmemberPATRemainsForbidden(t *testing.T) {
	guiDatabase(t)
	p := platformtest.Start(t)
	user := p.NewUser(t, "patcaller")
	own := p.NewOrg(t, user, "patown")
	other := p.NewUser(t, "patother")
	foreign := p.NewOrg(t, other, "patforeign")
	_, pat := p.NewPAT(t, user)
	// Pin the real mixed Identity outcomes, not a synthetic HTTP403 fixture.
	_, err := p.Identity.ResolveToken(context.Background(), &identityv1.ResolveTokenRequest{Token: pat, Org: foreign.Name})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("PAT identity code=%v, want PermissionDenied", status.Code(err))
	}
	_, err = p.Identity.ResolveSession(context.Background(), &identityv1.ResolveSessionRequest{Token: pat, Org: foreign.Name})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("PAT as session code=%v, want Unauthenticated", status.Code(err))
	}
	guiRequest(t, "GET", p.EdgeURL+"/api/v1/orgs/"+own.Name+"/repos", pat, nil, 200)
	denied := guiRequest(t, "GET", p.EdgeURL+"/api/v1/orgs/"+foreign.Name+"/repos", pat, nil, 403)
	missing := guiRequest(t, "GET", p.EdgeURL+"/api/v1/orgs/"+uuid.NewString()+"/repos", pat, nil, 403)
	if denied["error"] != missing["error"] {
		t.Fatal("denial distinguishes foreign organization membership from missing organization")
	}
	message, _ := denied["error"].(string)
	for _, private := range []string{pat, user.ID, user.Username, other.ID, other.Username, foreign.Name} {
		if strings.Contains(message, private) {
			t.Fatal("denial leaked credential or account/organization membership details")
		}
	}
	// The denial does not revoke or invalidate either credential kind.
	guiRequest(t, "GET", p.EdgeURL+"/api/v1/user", pat, nil, 200)
	guiRequest(t, "GET", p.EdgeURL+"/api/v1/orgs/"+own.Name+"/repos", user.Session, nil, 200)
	guiRequest(t, "GET", p.EdgeURL+"/api/v1/orgs/"+foreign.Name+"/repos", "invalid-fixture-credential", nil, 401)
}
