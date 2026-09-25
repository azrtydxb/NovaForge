package work_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestExecutionRPCRequiresVerifiedRuntimeIdentity(t *testing.T) {
	_, srv, human, _, _, req := executionFixture(t)
	sc, _ := authz.FromContext(human)
	secret := uuid.NewString()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(nil, secret)))
	workv1.RegisterWorkServiceServer(server, srv)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := workv1.NewWorkServiceClient(conn)
	mint := func(service string, org uuid.UUID) string {
		t.Helper()
		token, err := svcauth.Mint(secret, service, org, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	allowed := mint("agent-runtime", sc.OrgID)
	agent, err := svcauth.MintAgentRun(secret, sc.OrgID, uuid.New(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	platform, err := svcauth.MintPlatform(secret, "agent-runtime", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", allowed + "tampered", mint("ci-runner", sc.OrgID), mint("agent-runtime", uuid.New()), agent, platform} {
		ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
		if _, err := client.ClaimExecution(ctx, req); err == nil {
			t.Fatal("credential bypassed admission authority")
		}
	}
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+allowed)
	first, err := client.ClaimExecution(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.ClaimExecution(ctx, req)
	if err != nil || !proto.Equal(first, second) {
		t.Fatalf("RPC replay changed frozen intent: %v %v", second, err)
	}
	release := &workv1.ReleaseExecutionRequest{WorkItemId: req.WorkItemId, RepoId: req.RepoId, RunId: req.RunId, AgentId: req.AgentId, Outcome: "failed"}
	sibling := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+mint("ci-runner", sc.OrgID))
	if _, err := client.ReleaseExecution(sibling, release); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("sibling released claim: %v", err)
	}
	if _, err := client.ReleaseExecution(ctx, release); err != nil {
		t.Fatal(err)
	}
}
