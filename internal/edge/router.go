package edge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/authz"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	mcpv1 "github.com/novaforge/novaforge/gen/novaforge/mcp/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
)

// Config carries the service clients the edge fans out to. Any may be nil in
// tests that only exercise routing and authorization.
type Config struct {
	Identity identityv1.IdentityServiceClient
	Git      gitv1.GitServiceClient
	CI       civ1.CIServiceClient
	Work     workv1.WorkServiceClient
	Reviews  reviewsv1.ReviewsServiceClient
	Agents   agentsv1.AgentServiceClient
	Graph    graphv1.GraphServiceClient
	Gates    gatesv1.GatesServiceClient
	MCP      mcpv1.McpServiceClient
	Handlers map[string]http.HandlerFunc
}

// NewRouter builds the edge. Routing comes from the same table that generates
// the OpenAPI document, so a route cannot exist without a contract entry.
func NewRouter(cfg Config) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware)

	for _, rt := range Routes() {
		h := cfg.handlerFor(rt)
		if rt.Pattern == "/healthz" || strings.HasPrefix(rt.Pattern, "/api/v1/auth/") {
			r.Method(rt.Method, rt.Pattern, h)
			continue
		}
		r.Method(rt.Method, rt.Pattern, authenticate(cfg, h))
	}
	// Everything that is not a declared API route is the web application:
	// the app routes client-side, so a deep link reaches the server as a path
	// it has no route for. Registering it as the not-found handler means no
	// application path can shadow an API route.
	r.NotFound(WebHandler().ServeHTTP)

	return r
}

func (c Config) handlerFor(rt Route) http.Handler {
	if h, ok := c.Handlers[rt.OpID]; ok {
		return h
	}
	// A route with no handler wired is a programming error, not a 404: it must
	// be loud in tests rather than silently answering.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, http.StatusNotImplemented,
			fmt.Errorf("operation %s has no handler wired", rt.OpID))
	})
}

// authenticate resolves the caller through the identity service and installs an
// org-scoped authz.Scope. A request with no usable credential is refused; it is
// never treated as an unscoped, and therefore unrestricted, caller.
func authenticate(cfg Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.Identity == nil {
			WriteError(w, http.StatusUnauthorized, errors.New("no credentials"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		// A bearer header carries "a credential", not specifically a personal
		// access token: nf stores the session token from login and presents it
		// this way. Resolving against only one kind rejected every authenticated
		// CLI call, so both are tried before the request is refused.
		var subj *identityv1.Subject
		var lastErr error
		var presented string
		// The organization named in the path is the one the caller is acting in.
		// A credential says who you are, not which org you are acting in, so the
		// org travels with the request and identity verifies membership.
		org := chi.URLParam(r, "org")
		if tok := bearer(r); tok != "" {
			presented = tok
			if resp, err := cfg.Identity.ResolveToken(ctx, &identityv1.ResolveTokenRequest{Token: tok, Org: org}); err == nil {
				subj = resp.GetSubject()
			} else {
				lastErr = err
				if resp, err := cfg.Identity.ResolveSession(ctx, &identityv1.ResolveSessionRequest{Token: tok, Org: org}); err == nil {
					subj = resp.GetSubject()
					lastErr = nil
				} else {
					lastErr = err
				}
			}
		} else if ck, err := r.Cookie("nf_session"); err == nil && ck.Value != "" {
			presented = ck.Value
			resp, err := cfg.Identity.ResolveSession(ctx, &identityv1.ResolveSessionRequest{Token: ck.Value, Org: org})
			if err != nil {
				lastErr = err
			} else {
				subj = resp.GetSubject()
			}
		}
		if subj == nil && lastErr != nil {
			WriteError(w, http.StatusUnauthorized, lastErr)
			return
		}
		if subj == nil {
			WriteError(w, http.StatusUnauthorized, errors.New("no credentials"))
			return
		}

		scope := authz.Scope{ActorKind: subj.GetActorKind(), Role: subj.GetRole()}
		if id, err := uuid.Parse(subj.GetUserId()); err == nil {
			scope.ActorID = id
		}
		if id, err := uuid.Parse(subj.GetOrgId()); err == nil {
			scope.OrgID = id
		}

		// The org named in the path is deliberately not checked here.
		// Authorization lives at the data layer: each service resolves the org
		// within the caller's scope and answers PermissionDenied or NotFound,
		// which StatusFromGRPC maps to 403 or 404. Duplicating the check here
		// would mean two places to get it wrong, and the edge is the one
		// without the data to decide.
		// The credential travels with the request so downstream gRPC calls can
		// present it; the edge carries the caller's identity rather than acting
		// as a trusted principal of its own.
		rctx := authz.WithScope(r.Context(), scope)
		rctx = WithCredential(rctx, presented)
		rctx = WithOrgRef(rctx, org)
		next.ServeHTTP(w, r.WithContext(rctx))
	})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// WriteError renders an error as JSON with the given status.
func WriteError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// WriteJSON renders v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// StatusFromGRPC maps a gRPC status onto the HTTP status the contract promises.
func StatusFromGRPC(err error) int {
	switch status.Code(err) {
	case codes.OK:
		return http.StatusOK
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists:
		return http.StatusConflict
	case codes.InvalidArgument:
		return http.StatusBadRequest
	case codes.FailedPrecondition:
		// A merge the gates refuse, a gate proposal that changes nothing,
		// cancelling a finished run, deciding a proposal twice: a legitimate
		// question whose answer is no, not a server fault.
		return http.StatusConflict
	case codes.Unimplemented:
		// Services say "this deployment has no X" with Unimplemented; 501 is
		// what the GUI reads as not available rather than as broken.
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
