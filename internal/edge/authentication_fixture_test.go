package edge_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/edge"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Inject errors at the Identity RPC, not as pretranslated HTTP fixtures: the
// production edge must decide whether the browser sees 401, 503 or 504.
// Used only by the explicitly requested, isolated TestGUIBrowserFixture.
type failingIdentityRPC struct {
	identityv1.UnimplementedIdentityServiceServer
	upstream identityv1.IdentityServiceClient
	modePath string
}

func (s *failingIdentityRPC) failure() error {
	raw, _ := os.ReadFile(s.modePath)
	switch strings.TrimSpace(string(raw)) {
	case "unavailable":
		return status.Error(codes.Unavailable, "identity transport unavailable")
	case "deadline":
		return status.Error(codes.DeadlineExceeded, "identity resolution deadline")
	default:
		return nil
	}
}
func (s *failingIdentityRPC) ResolveToken(ctx context.Context, r *identityv1.ResolveTokenRequest) (*identityv1.ResolveTokenResponse, error) {
	if err := s.failure(); err != nil {
		return nil, err
	}
	return s.upstream.ResolveToken(ctx, r)
}
func (s *failingIdentityRPC) ResolveSession(ctx context.Context, r *identityv1.ResolveSessionRequest) (*identityv1.ResolveSessionResponse, error) {
	if err := s.failure(); err != nil {
		return nil, err
	}
	return s.upstream.ResolveSession(ctx, r)
}

func authenticationFixtureEdge(t *testing.T, dir, upstreamURL string, identity identityv1.IdentityServiceClient) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	identityv1.RegisterIdentityServiceServer(server, &failingIdentityRPC{upstream: identity, modePath: filepath.Join(dir, "identity-mode")})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	target, err := url.Parse(upstreamURL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	handlers := map[string]http.HandlerFunc{}
	for _, route := range edge.Routes() {
		handlers[route.OpID] = proxy.ServeHTTP
	}
	httpServer := httptest.NewServer(edge.NewRouter(edge.Config{Identity: identityv1.NewIdentityServiceClient(conn), Handlers: handlers}))
	t.Cleanup(httpServer.Close)
	return httpServer.URL
}
