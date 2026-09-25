package edge_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/edge"
	"github.com/novaforge/novaforge/internal/platformtest"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type cursorAgentOwner struct {
	agentsv1.UnimplementedAgentServiceServer
	org, run, call string
	mu             sync.Mutex
	received       []string
}

func (s *cursorAgentOwner) GetRun(ctx context.Context, r *agentsv1.GetRunRequest) (*agentsv1.GetRunResponse, error) {
	sc, err := authz.FromContext(ctx)
	if err != nil || sc.OrgID.String() != s.org || r.Id != s.run {
		return nil, status.Error(codes.NotFound, "run not found")
	}
	return &agentsv1.GetRunResponse{Run: &agentsv1.Run{Id: s.run, OrgId: s.org}}, nil
}
func (s *cursorAgentOwner) StreamRunEvents(r *agentsv1.StreamRunEventsRequest, stream agentsv1.AgentService_StreamRunEventsServer) error {
	if _, err := s.GetRun(stream.Context(), &agentsv1.GetRunRequest{Id: r.RunId}); err != nil {
		return err
	}
	s.mu.Lock()
	s.received = append(s.received, r.AfterCursor)
	s.mu.Unlock()
	if r.AfterCursor == s.run+":99" {
		return status.Error(codes.InvalidArgument, "cursor unavailable")
	}
	if err := stream.SendHeader(metadata.Pairs("x-novaforge-stream-ready", "1")); err != nil {
		return err
	}
	if r.AfterCursor == s.run+":2" {
		return status.Error(codes.Unavailable, "tool history unavailable")
	}
	if r.AfterCursor == "" {
		if err := stream.Send(&agentsv1.StreamRunEventsResponse{RunId: s.run, Cursor: s.run + ":1", ToolCallId: s.call, Payload: &agentsv1.StreamRunEventsResponse_ToolCall{ToolCall: &agentsv1.ToolCall{Tool: "repo.read_file", Outcome: "pending"}}}); err != nil {
			return err
		}
	}
	if err := stream.Send(&agentsv1.StreamRunEventsResponse{RunId: s.run, Payload: &agentsv1.StreamRunEventsResponse_StateChange{StateChange: &agentsv1.StateChange{FromState: "queued", ToState: "running"}}}); err != nil {
		return err
	}
	return stream.Send(&agentsv1.StreamRunEventsResponse{RunId: s.run, Cursor: s.run + ":2", ToolCallId: s.call, Payload: &agentsv1.StreamRunEventsResponse_ToolCall{ToolCall: &agentsv1.ToolCall{Tool: "repo.read_file", Outcome: "ok"}}})
}

// Runtime replay semantics are covered by its owner tests. This seam test uses
// the frozen protocol over real authenticated gRPC and real Identity sessions.
func TestAuthenticatedAgentSSECursorContract(t *testing.T) {
	guiDatabase(t)
	p := platformtest.Start(t)
	user := p.NewUser(t, "cursor")
	org := p.NewOrg(t, user, "cursororg")
	foreign := p.NewOrg(t, user, "cursorforeign")
	owner := &cursorAgentOwner{org: org.ID, run: uuid.NewString(), call: uuid.NewString()}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(p.Identity, platformtest.HMACSecret)), grpc.StreamInterceptor(svcauth.StreamServerInterceptor(p.Identity, platformtest.HMACSecret)))
	agentsv1.RegisterAgentServiceServer(server, owner)
	go server.Serve(l)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(l.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithUnaryInterceptor(edge.ForwardCredential), grpc.WithStreamInterceptor(edge.ForwardCredentialStream))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	cfg := edge.Config{Identity: p.Identity, Agents: agentsv1.NewAgentServiceClient(conn)}
	cfg.Handlers = edge.Handlers(cfg)
	httpServer := httptest.NewServer(edge.NewRouter(cfg))
	t.Cleanup(httpServer.Close)
	base := httpServer.URL + "/api/v1/orgs/" + org.Name + "/agent-runs/" + owner.run + "/events"
	request := func(path, token, cursor string, want int) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r, _ := http.NewRequestWithContext(ctx, "GET", path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Last-Event-ID", cursor)
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != want {
			t.Fatalf("status %d want %d: %s", res.StatusCode, want, raw)
		}
		return string(raw)
	}
	request(base, "", "", 401)
	request(base, "invalid-token", owner.run+":1", 401)
	request(strings.Replace(base, org.Name, foreign.Name, 1), user.Session, "", 404)
	raw := request(base, user.Session, "", 200)
	if !strings.Contains(raw, "id: "+owner.run+":1\n") || !strings.Contains(raw, "id: "+owner.run+":2\n") || !strings.Contains(raw, `"tool_call_id":"`+owner.call+`"`) {
		t.Errorf("missing cursor/call identity: %s", raw)
	}
	for _, frame := range strings.Split(raw, "\n\n") {
		if strings.Contains(frame, "state_change") && strings.Contains(frame, "id:") {
			t.Errorf("state event resets tool cursor: %s", frame)
		}
	}
	raw = request(base, user.Session, owner.run+":1", 200)
	if strings.Contains(raw, "id: "+owner.run+":1\n") || !strings.Contains(raw, "id: "+owner.run+":2\n") {
		t.Errorf("reconnect not exclusive: %s", raw)
	}
	request(base, user.Session, owner.run+":99", 400)
	lost := request(base, user.Session, owner.run+":2", 200)
	if !strings.Contains(lost, "event: error") || !strings.Contains(lost, "tool history unavailable") || strings.Contains(lost, "id:") {
		t.Errorf("retention loss hidden or cursor reset: %s", lost)
	}
	for _, invalid := range []string{"bad", owner.run + ":0", uuid.NewString() + ":1", strings.Repeat("x", 65)} {
		request(base, user.Session, invalid, 400)
	}
	request(base+"?after_cursor="+owner.run+":1", user.Session, "", 200)
	request(base+"?after_cursor="+owner.run+":1", user.Session, owner.run+":2", 400)
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if len(owner.received) != 5 || owner.received[1] != owner.run+":1" || owner.received[4] != owner.run+":1" {
		t.Errorf("owner cursors: %v", owner.received)
	}
}
