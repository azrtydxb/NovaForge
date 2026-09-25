package deployment

import (
	"context"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	deploymentv1 "github.com/novaforge/novaforge/gen/novaforge/deployment/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestDeploymentAuthenticatedRPC(t *testing.T) {
	s, ctx, admin, req, exec := fixture(t)
	key := uuid.NewString()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(nil, key)))
	deploymentv1.RegisterDeploymentServiceServer(server, NewGRPCServer(s))
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	wire := deploymentv1.NewDeploymentServiceClient(conn)
	client := NewClient(wire)
	scope, _ := authz.FromContext(ctx)
	token, err := svcauth.MintAgentRun(key, scope.OrgID, scope.ActorID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	call := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	if _, err := client.Request(context.Background(), req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous request: %v", err)
	}
	op, err := client.Request(call, req)
	if err != nil {
		t.Fatal(err)
	}
	if op.ID != req.ID || op.OrgID != scope.OrgID || op.Artifact != req.Artifact || op.TargetRevision != "fixture-v1" {
		t.Fatalf("intent lost: %+v", op)
	}
	if _, err := client.Execute(call, op.ID); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("pending executed: %v", err)
	}
	if exec.calls.Load() != 0 {
		t.Fatal("executor called before approval")
	}
	approve(t, s, admin, op)
	op, err = client.Execute(call, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != StateSucceeded || len(op.Attempts) != 1 || op.Attempts[0].Result.ExternalID != req.ID.String() {
		t.Fatalf("missing durable evidence: %+v", op)
	}
	stored, err := s.Get(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Empty slices are normalized by the RPC adapter; compare encoded metadata.
	if !reflect.DeepEqual(operationProto(op), operationProto(stored)) {
		t.Fatal("RPC operation lost durable metadata")
	}
	if _, err = client.Execute(call, op.ID); err != nil || exec.calls.Load() != 1 {
		t.Fatal("RPC replay reexecuted", err)
	}
	foreign, _ := svcauth.MintAgentRun(key, uuid.New(), scope.ActorID, time.Minute)
	foreignCtx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+foreign))
	if _, err = client.Get(foreignCtx, op.ID); status.Code(err) != codes.NotFound {
		t.Fatalf("foreign operation visible: %v", err)
	}
	sibling, _ := svcauth.Mint(key, "ci-credentials", scope.OrgID, time.Minute)
	siblingCtx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+sibling))
	if _, err = client.Get(siblingCtx, op.ID); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("sibling admitted: %v", err)
	}
	if _, err = wire.GetDeployment(call, &deploymentv1.GetDeploymentRequest{Id: "bad"}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	req.Artifact = "sha256:" + strings.Repeat("b", 64)
	if _, err = client.Request(call, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("intent conflict accepted", err)
	}
}

func TestDeploymentOperationRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	done := now.Add(time.Second)
	op := Operation{Request: Request{ID: uuid.New(), RunID: uuid.New(), RepoID: uuid.New(), Target: "prod", Artifact: "sha256:" + strings.Repeat("a", 64)}, OrgID: uuid.New(), ActorID: uuid.New(), ActorKind: "agent", Environment: "production", TargetRevision: "revision", Destination: "fixed", State: StateUncertain, CreatedAt: now,
		Attempts:    []Attempt{{ActorID: uuid.New(), ActorKind: "user", Kind: "recover", Number: 2, State: StateUncertain, StartedAt: now, FinishedAt: &done, Result: Result{ExternalID: "release/1", Summary: "verified"}, Error: "cleanup pending"}},
		Credentials: []CredentialObligation{{Attempt: 1, ProviderBinding: "revision", Phase: "dispatching", ResolvedAt: &done}, {Attempt: 2, ProviderBinding: "revision", Phase: "preparing"}}}
	got, err := operationFromProto(operationProto(op))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(op, got) {
		t.Fatalf("metadata mismatch: %#v vs %#v", op, got)
	}
	if _, err := operationFromProto(nil); err == nil {
		t.Fatal("missing response accepted")
	}
}
