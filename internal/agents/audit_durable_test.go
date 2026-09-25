package agents_test

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"net"
	"strings"
	"testing"
	"time"
)

func TestAuditRecordBindsParentOrganization(t *testing.T) {
	s := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run := mustCreateRun(t, s, ctx, org)
	_, err := agents.NewAuditLog(s.Pool()).Record(scopedCtx(uuid.New()), agents.Entry{RunID: run.ID, Tool: "repo.read_file", ArgsJSON: []byte(`{}`)})
	if err == nil {
		t.Fatal("foreign parent accepted")
	}
}
func TestAuditObservableMetadataAndOutboxAtomic(t *testing.T) {
	s := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run := mustCreateRun(t, s, ctx, org)
	a := agents.NewAuditLog(s.Pool())
	id, err := a.Record(ctx, agents.Entry{RunID: run.ID, Tool: "repo.read_file", ArgsJSON: []byte(`{"path":"credential-secret","bearer":"credential-secret"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Complete(ctx, id, "error", "upstream credential-secret"); err != nil {
		t.Fatal(err)
	}
	rows, err := a.List(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rows[0].ArgsJSON)+rows[0].Error, "credential-secret") {
		t.Fatal("plaintext persisted")
	}
	var n int
	if err = s.Pool().QueryRow(context.Background(), `SELECT count(*) FROM agents.tool_events WHERE call_id=$1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want start and terminal events, got %d", n)
	}
	if err = a.Complete(ctx, id, "error", "upstream credential-secret"); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool().QueryRow(ctx, `SELECT count(*) FROM agents.tool_events WHERE call_id=$1`, id).Scan(&n); err != nil || n != 2 {
		t.Fatalf("replay duplicated events: %d %v", n, err)
	}
}

func TestToolEventReplayRestartAndReconciliation(t *testing.T) {
	s := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run := mustCreateRun(t, s, ctx, org)
	a := agents.NewAuditLog(s.Pool())
	rdb := agentsRedis(t)
	call := uuid.New()
	entry := agents.Entry{ID: call, RunID: run.ID, Tool: "repo.read_file", ArgsJSON: []byte(`{"secret":"hidden"}`)}
	if _, err := a.Record(ctx, entry); err != nil {
		t.Fatal(err)
	}
	// Replay the exact start after an ambiguous commit response, then restart.
	if _, err := a.Record(ctx, entry); err != nil {
		t.Fatal(err)
	}
	a = agents.NewAuditLog(s.Pool())
	before, err := a.ToolEvents(ctx, run.ID, "")
	if err != nil || len(before) != 1 {
		t.Fatalf("start replay: %+v %v", before, err)
	}
	if err = a.Complete(ctx, call, "cancelled", "secret in upstream error"); err != nil {
		t.Fatal(err)
	}
	after, err := a.ToolEvents(ctx, run.ID, before[0].Cursor)
	if err != nil || len(after) != 1 || after[0].CallID != call || after[0].Outcome != "cancelled" {
		t.Fatalf("exclusive resume: %+v %v", after, err)
	}
	for _, cursor := range []string{uuid.NewString() + ":1", run.ID.String() + ":999999999", run.ID.String() + ":-1", strings.Repeat("x", 100)} {
		if _, err = a.ToolEvents(ctx, run.ID, cursor); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
	if _, err = a.ToolEvents(scopedCtx(uuid.New()), run.ID, after[0].Cursor); err == nil {
		t.Fatal("foreign cursor accepted")
	}
	if err = a.MaintainAudit(ctx, rdb); err != nil {
		t.Fatal(err)
	}
	// A lost publisher acknowledgement forces duplicate delivery with same identity.
	if _, err = s.Pool().Exec(ctx, `UPDATE agents.tool_events SET published=false WHERE org_id=$1 AND run_id=$2`, org, run.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.MaintainAudit(ctx, rdb); err != nil {
		t.Fatal(err)
	}
	replay, err := a.ToolEvents(ctx, run.ID, "")
	if err != nil || len(replay) != 2 {
		t.Fatalf("duplicate notification affected history: %v %v", replay, err)
	}
	messages, err := rdb.XRange(ctx, "stream:agent:events", "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, m := range messages {
		if strings.Contains(fmt.Sprint(m.Values["data"]), call.String()) {
			count++
			if strings.Contains(fmt.Sprint(m.Values), "hidden") {
				t.Fatal("secret in Redis")
			}
		}
	}
	if count != 4 {
		t.Fatalf("want duplicate stable notifications, got %d", count)
	}
	pending, err := a.Record(ctx, agents.Entry{RunID: run.ID, Tool: "work.get", ArgsJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.ReconcileRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool().Exec(ctx, `UPDATE agents.agent_runs SET execution_finished=true WHERE org_id=$1 AND id=$2`, org, run.ID); err != nil {
		t.Fatal(err)
	}
	if err = agents.NewAuditLog(s.Pool()).ReconcileRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.Complete(ctx, pending, "ok", ""); err == nil {
		t.Fatal("missing outcome relabelled successful")
	}
}

func TestToolEventsAuthenticatedStreamReconnect(t *testing.T) {
	s := newStore(t)
	org := uuid.New()
	ctx := scopedCtx(org)
	run := mustCreateRun(t, s, ctx, org)
	a := agents.NewAuditLog(s.Pool())
	rdb := agentsRedis(t)
	id, err := a.Record(ctx, agents.Entry{RunID: run.ID, Tool: "repo.read_file", ArgsJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Complete(ctx, id, "denied", "do not publish token"); err != nil {
		t.Fatal(err)
	}
	const secret = "owned-audit-stream"
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.StreamInterceptor(svcauth.StreamServerInterceptor(nil, secret)))
	agentsv1.RegisterAgentServiceServer(srv, agents.NewGRPCServer(s, nil, rdb, nil, nil))
	go srv.Serve(lis)
	defer srv.Stop()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := agentsv1.NewAgentServiceClient(conn)
	call := func(org uuid.UUID, cursor string) (*agentsv1.StreamRunEventsResponse, error) {
		token, e := svcauth.Mint(secret, "edge-test", org, time.Minute)
		if e != nil {
			return nil, e
		}
		c, cancel := context.WithTimeout(metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token)), 3*time.Second)
		defer cancel()
		stream, e := client.StreamRunEvents(c, &agentsv1.StreamRunEventsRequest{RunId: run.ID.String(), AfterCursor: cursor})
		if e != nil {
			return nil, e
		}
		headers, e := stream.Header()
		if e != nil {
			return nil, e
		}
		response, e := stream.Recv()
		if e == nil {
			ready := headers.Get("x-novaforge-stream-ready")
			if len(ready) != 1 || ready[0] != "1" {
				t.Fatalf("validated owner stream lacks explicit readiness marker: %v", headers)
			}
		}
		return response, e
	}
	first, err := call(org, "")
	if err != nil || first.GetToolCall().GetOutcome() != "pending" {
		t.Fatalf("start: %v %v", first, err)
	}
	next, err := call(org, first.GetCursor())
	if err != nil || next.GetToolCall().GetOutcome() != "denied" || next.GetToolCallId() != first.GetToolCallId() {
		t.Fatalf("resume: %v %v", next, err)
	}
	if _, err = call(uuid.New(), first.GetCursor()); status.Code(err) != codes.NotFound {
		t.Fatalf("foreign reconnect: %v", err)
	}
	if _, err = call(org, run.ID.String()+":9999999"); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown cursor: %v", err)
	}
}
