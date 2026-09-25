package ci_test

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	"github.com/novaforge/novaforge/internal/ci"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type activationBarrier struct {
	labels                       atomic.Int32
	auth                         atomic.Int32
	entered, release, secondAuth chan struct{}
}

func (b *activationBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SELECT org_id, token_hash") && b.auth.Add(1) == 2 {
		close(b.secondAuth)
	}
	if strings.Contains(data.SQL, "SELECT labels FROM ci.runners") && b.labels.Add(1) == 1 {
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	}
	return ctx
}
func (*activationBarrier) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestDelayedConnectActivationCannotDisplaceNewerConnection(t *testing.T) {
	s := newCredentialStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	barrier := &activationBarrier{entered: make(chan struct{}), release: make(chan struct{}), secondAuth: make(chan struct{})}
	cfg := s.pool.Config()
	cfg.ConnConfig.Tracer = barrier
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := ci.NewStore(pool)
	dispatcher := ci.NewDispatcher(store)
	server := ci.NewServer(store, dispatcher)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpc := grpc.NewServer()
	civ1.RegisterRunnerServiceServer(rpc, server)
	go rpc.Serve(lis)
	defer rpc.Stop()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := civ1.NewRunnerServiceClient(conn)
	first, err := client.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hello := &civ1.ConnectRequest{RunnerId: s.runnerID.String(), Token: s.runnerToken, Payload: &civ1.ConnectRequest_Heartbeat{Heartbeat: &civ1.Heartbeat{}}}
	if err := first.Send(hello); err != nil {
		t.Fatal(err)
	}
	select {
	case <-barrier.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	second, err := client.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Send(hello); err != nil {
		t.Fatal(err)
	}
	select {
	case <-barrier.secondAuth:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Give the unfenced implementation a chance to register B while A's
	// labels query is deterministically held. Correct activation serializes it.
	secondHello := make(chan *civ1.ConnectResponse, 1)
	go func() { msg, _ := second.Recv(); secondHello <- msg }()
	var b *civ1.ConnectResponse
	select {
	case b = <-secondHello:
	case <-time.After(150 * time.Millisecond):
	}
	close(barrier.release)
	if _, err := first.Recv(); err != nil {
		t.Fatal(err)
	}
	if b == nil {
		select {
		case b = <-secondHello:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if b.GetConnectionId() == "" {
		t.Fatal("replacement handshake failed")
	}
	connected := dispatcher.Connected()
	if len(connected) != 1 || connected[0].ConnectionID.String() != b.ConnectionId {
		t.Fatalf("older activation displaced authoritative replacement: %+v", connected)
	}
	job := s.job("refs/heads/main", "replacement-delivery", "staging")
	pump := ci.NewPump(store, dispatcher, "", "unused")
	go pump.Run(ctx)
	delivered, err := second.Recv()
	if err != nil || delivered.GetJobId() != job.String() {
		t.Fatalf("current connection cannot receive job: %v", err)
	}
}
