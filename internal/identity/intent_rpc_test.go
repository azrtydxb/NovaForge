package identity_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/identity"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func rpcListener(t *testing.T, register func(*grpc.Server), interceptor grpc.UnaryServerInterceptor) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.UnaryInterceptor(interceptor))
	register(srv)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestStableIntentAuthenticatedOwnerRPC(t *testing.T) {
	pool := storePool(t)
	rdb := newRedisClient(t)
	if err := database.Migrate(dbURL(t), "agents", agents.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(dbURL(t), "gitplatform", capability.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	const secret = "owned-identity-intent-rpc"
	identities := identity.NewStore(pool)
	sessions := identity.NewSessionStore(rdb)
	sponsor, err := identities.CreateUser(context.Background(), uuid.NewString()+"@example.com", uuid.NewString(), "hash")
	if err != nil {
		t.Fatal(err)
	}
	org, err := identities.CreateOrg(context.Background(), uuid.NewString(), sponsor.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope := authz.WithScope(context.Background(), authz.Scope{OrgID: org.ID, ActorID: sponsor.ID, ActorKind: "user"})
	runs := agents.NewStore(pool)
	agent, err := runs.CreateAgent(scope, agents.Agent{OrgID: org.ID, Name: "owner-rpc", Role: "coder", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	idServer := identity.NewGRPCServer(identities, sessions, identity.NewTokenStore(pool), nil, capability.NewStore(pool))
	idServer.HMACSecret = secret
	idConn := rpcListener(t, func(s *grpc.Server) { identityv1.RegisterIdentityServiceServer(s, idServer) }, identity.UnaryAuthInterceptor(idServer))
	client := identityv1.NewIdentityServiceClient(idConn)
	agentServer := agents.NewGRPCServer(runs, nil, rdb, nil, nil)
	agentConn := rpcListener(t, func(s *grpc.Server) { agentsv1.RegisterAgentServiceServer(s, agentServer) }, svcauth.UnaryServerInterceptor(client, secret))
	idServer.Agents = agentsv1.NewAgentServiceClient(agentConn)
	asService := func(name string, orgID uuid.UUID) context.Context {
		tok, e := svcauth.Mint(secret, name, orgID, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		return metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+tok))
	}
	makeIntent := func() capability.IssuanceIntent {
		i := capability.IssuanceIntent{IssuerID: sponsor.ID, IssuerKind: "user", Grant: capability.Grant{ID: uuid.New(), OrgID: org.ID, SubjectID: agent.ID, SubjectKind: "agent", RepoRead: true, WriteBranch: "agents/NF-1/", ExpiresAt: time.Now().Add(time.Hour)}}
		r, e := runs.CreateRun(scope, agents.Run{OrgID: org.ID, RepoID: uuid.New(), AgentID: agent.ID, WorkItemID: uuid.New(), SponsorID: sponsor.ID, GrantID: i.Grant.ID, GrantIntent: &i, Branch: "agents/NF-1/work", WallclockLimit: time.Hour})
		if e != nil {
			t.Fatal(e)
		}
		return *r.GrantIntent
	}
	i := makeIntent()
	runtime := asService("agent-runtime", org.ID)
	for _, ctx := range []context.Context{context.Background(), asService("gates", org.ID), asService("agent-runtime", uuid.New())} {
		if _, err = client.IssueIntent(ctx, &identityv1.IssueIntentRequest{Intent: capability.IntentProto(i)}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("untrusted issuance: %v", err)
		}
	}
	issued, err := client.IssueIntent(runtime, &identityv1.IssueIntentRequest{Intent: capability.IntentProto(i)})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := client.IssueIntent(runtime, &identityv1.IssueIntentRequest{Intent: capability.IntentProto(i)})
	if err != nil || replay.GetGrant().GetExpiresAt() != issued.GetGrant().GetExpiresAt() {
		t.Fatalf("stable replay: %v", err)
	}
	mismatch := i
	mismatch.Grant.SubjectID = uuid.New()
	if _, err = client.IssueIntent(runtime, &identityv1.IssueIntentRequest{Intent: capability.IntentProto(mismatch)}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("subject swap: %v", err)
	}
	for _, name := range []string{"gates", "ci-credentials", "agent-runtime"} {
		if _, err = client.GetGrant(asService(name, org.ID), &identityv1.GetGrantRequest{Id: i.Grant.ID.String()}); err != nil {
			t.Fatalf("resolver %s: %v", name, err)
		}
	}
	if _, err = client.GetGrant(asService("unrelated", org.ID), &identityv1.GetGrantRequest{Id: i.Grant.ID.String()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("sibling resolver: %v", err)
	}
	if _, err = client.GetGrant(asService("gates", uuid.New()), &identityv1.GetGrantRequest{Id: i.Grant.ID.String()}); status.Code(err) != codes.NotFound {
		t.Fatalf("foreign resolver: %v", err)
	}
	// Cleanup uses the persisted owner receipt, not a still-valid human session.
	if _, err = pool.Exec(context.Background(), `DELETE FROM identity.org_members WHERE org_id=$1 AND user_id=$2`, org.ID, sponsor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = client.CancelIssuance(runtime, &identityv1.CancelIssuanceRequest{Intent: capability.IntentProto(i)}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.GetGrant(asService("gates", org.ID), &identityv1.GetGrantRequest{Id: i.Grant.ID.String()}); status.Code(err) != codes.NotFound {
		t.Fatalf("revoked grant resolved: %v", err)
	}
	if err = identities.AddOrgMember(context.Background(), org.ID, sponsor.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.IssueIntent(runtime, &identityv1.IssueIntentRequest{Intent: capability.IntentProto(i)}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("late issue resurrected: %v", err)
	}
	before := makeIntent()
	if _, err = client.CancelIssuance(runtime, &identityv1.CancelIssuanceRequest{Intent: capability.IntentProto(before)}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.IssueIntent(runtime, &identityv1.IssueIntentRequest{Intent: capability.IntentProto(before)}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("cancel-before-issue resurrected: %v", err)
	}
	if _, err = idServer.Agents.GetGrantIntent(asService("gates", org.ID), &agentsv1.GetGrantIntentRequest{RunId: i.RunID.String()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("receipt disclosed: %v", err)
	}
}
