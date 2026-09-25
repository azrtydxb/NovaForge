package identity_test

import (
	"context"
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
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestLegacyGrantRevokeAuthenticatedOwnerBinding(t *testing.T) {
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

	authority := capability.NewStore(pool)
	makeLegacy := func(state string) (capability.Grant, agents.Run) {
		prefix := "agents/" + uuid.NewString() + "/"
		grant, e := authority.Issue(scope, capability.Grant{OrgID: org.ID, SubjectID: agent.ID, SubjectKind: "agent", RepoRead: true, WriteBranch: prefix, ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		run, e := runs.CreateRun(scope, agents.Run{OrgID: org.ID, AgentID: agent.ID, SponsorID: sponsor.ID, GrantID: grant.ID, Branch: prefix + "work", WallclockLimit: time.Hour})
		if e != nil {
			t.Fatal(e)
		}
		if state != "queued" {
			if e = runs.SetRunState(scope, run.ID, "running"); e != nil {
				t.Fatal(e)
			}
		}
		if state == "failed" {
			if _, e = runs.CompleteRun(scope, run.ID, agents.Completion{State: "failed"}); e != nil {
				t.Fatal(e)
			}
		}
		return grant, run
	}
	for _, state := range []string{"queued", "running", "failed"} {
		grant, run := makeLegacy(state)
		if _, err = client.RevokeGrant(asService("gates", org.ID), &identityv1.RevokeGrantRequest{Id: grant.ID.String()}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("sibling revoke: %v", err)
		}
		// Membership removal cannot obstruct cleanup; owner binding is persisted.
		if _, err = pool.Exec(scope, `DELETE FROM identity.org_members WHERE org_id=$1 AND user_id=$2`, org.ID, sponsor.ID); err != nil {
			t.Fatal(err)
		}
		runtime := asService("agent-runtime", org.ID)
		if _, err = client.RevokeGrant(runtime, &identityv1.RevokeGrantRequest{Id: grant.ID.String()}); err != nil {
			t.Fatalf("legacy %s: %v", state, err)
		}
		if _, err = pool.Exec(scope, `DELETE FROM agents.agent_runs WHERE id=$1 AND org_id=$2`, run.ID, org.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = authority.ResolveScoped(scope, grant.ID); err == nil {
			t.Fatal("legacy grant still live")
		}
		if _, err = client.RevokeGrant(runtime, &identityv1.RevokeGrantRequest{Id: grant.ID.String()}); err != nil {
			t.Fatalf("legacy replay: %v", err)
		}
		var n int
		if err = pool.QueryRow(scope, `SELECT count(*) FROM gitplatform.capability_issuances WHERE id=$1`, grant.ID).Scan(&n); err != nil || n != 0 {
			t.Fatal("historical issuance fabricated", err)
		}
		if _, err = pool.Exec(scope, `DELETE FROM gitplatform.capability_grants WHERE id=$1`, grant.ID); err != nil {
			t.Fatal(err)
		}
		i := capability.IssuanceIntent{RunID: run.ID, IssuerID: sponsor.ID, IssuerKind: "user", Grant: grant}
		if _, err = authority.IssueIntent(scope, i); err == nil {
			t.Fatal("delayed issuer resurrected cancelled legacy id")
		}
	}

	// No run receipt is not equivalent to a cleaned-up legacy grant.
	unbound, e := authority.Issue(scope, capability.Grant{OrgID: org.ID, SubjectID: agent.ID, SubjectKind: "agent", WriteBranch: "agents/unbound/", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	if _, err = client.RevokeGrant(asService("agent-runtime", org.ID), &identityv1.RevokeGrantRequest{Id: unbound.ID.String()}); err == nil {
		t.Fatal("unbound legacy grant accepted")
	}
	ambiguous, original := makeLegacy("queued")
	if _, err = runs.CreateRun(scope, agents.Run{OrgID: org.ID, AgentID: agent.ID, SponsorID: sponsor.ID, GrantID: ambiguous.ID, Branch: original.Branch}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.RevokeGrant(asService("agent-runtime", org.ID), &identityv1.RevokeGrantRequest{Id: ambiguous.ID.String()}); err == nil {
		t.Fatal("ambiguous run binding accepted")
	}
	wrong, wrongRun := makeLegacy("queued")
	other, e := runs.CreateAgent(scope, agents.Agent{OrgID: org.ID, Name: uuid.NewString(), Role: "coder", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, err = pool.Exec(scope, `UPDATE agents.agent_runs SET agent_id=$1 WHERE id=$2`, other.ID, wrongRun.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = client.RevokeGrant(asService("agent-runtime", org.ID), &identityv1.RevokeGrantRequest{Id: wrong.ID.String()}); err == nil {
		t.Fatal("wrong persisted subject accepted")
	}
	bad, run := makeLegacy("queued")
	if _, err = pool.Exec(scope, `UPDATE agents.agent_runs SET agent_id=$1 WHERE id=$2`, uuid.New(), run.ID); err == nil {
		t.Fatal("fixture expected agent FK")
	}
	if _, err = pool.Exec(scope, `UPDATE agents.agent_runs SET branch='agents/other/work' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = client.RevokeGrant(asService("agent-runtime", org.ID), &identityv1.RevokeGrantRequest{Id: bad.ID.String()}); err == nil {
		t.Fatal("wrong branch accepted")
	}
	if _, err = client.RevokeGrant(asService("agent-runtime", uuid.New()), &identityv1.RevokeGrantRequest{Id: bad.ID.String()}); err == nil {
		t.Fatal("foreign org accepted")
	}
	if _, err = client.RevokeGrant(asService("agent-runtime", org.ID), &identityv1.RevokeGrantRequest{Id: uuid.NewString()}); err == nil {
		t.Fatal("missing grant accepted")
	}
}
