package mcp_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	mcpv1 "github.com/novaforge/novaforge/gen/novaforge/mcp/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/identity"
	"github.com/novaforge/novaforge/internal/mcp"
)

// identityDirect presents the real identity server as a client, in process,
// so the role check runs against real org_members rows exactly as it would
// over gRPC. Only ListOrgMembers is reachable; anything else panics through
// the nil embedded interface rather than answering.
type identityDirect struct {
	identityv1.IdentityServiceClient
	srv *identity.Server
}

func (d identityDirect) ListOrgMembers(ctx context.Context, in *identityv1.ListOrgMembersRequest, _ ...grpc.CallOption) (*identityv1.ListOrgMembersResponse, error) {
	return d.srv.ListOrgMembers(ctx, in)
}

type registryFixture struct {
	reg                   *mcp.Registry
	org                   uuid.UUID
	owner, admin, member  context.Context
	outsider, agent, none context.Context
}

func newRegistryFixture(t *testing.T) registryFixture {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := database.Migrate(url, "identity", identity.MigrationsFS); err != nil {
		t.Fatalf("migrate identity: %v", err)
	}
	if err := database.Migrate(url, "mcp", mcp.MigrationsFS); err != nil {
		t.Fatalf("migrate mcp: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	ctx := context.Background()
	ids := identity.NewStore(pool)
	user := func(prefix string) uuid.UUID {
		s := uuid.NewString()[:8]
		u, err := ids.CreateUser(ctx, prefix+s+"@example.com", prefix+s, "hash")
		if err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		return u.ID
	}
	ownerID, adminID, memberID := user("owner"), user("admin"), user("member")
	org, err := ids.CreateOrg(ctx, "mcporg-"+uuid.NewString()[:8], ownerID)
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if err := ids.AddOrgMember(ctx, org.ID, adminID, "admin"); err != nil {
		t.Fatalf("AddOrgMember admin: %v", err)
	}
	if err := ids.AddOrgMember(ctx, org.ID, memberID, "member"); err != nil {
		t.Fatalf("AddOrgMember member: %v", err)
	}

	as := func(id uuid.UUID, kind string, orgID uuid.UUID) context.Context {
		return authz.WithScope(ctx, authz.Scope{OrgID: orgID, ActorID: id, ActorKind: kind})
	}
	idSrv := identity.NewGRPCServer(ids, nil, nil, nil, nil)
	return registryFixture{
		reg:      mcp.NewRegistry(pool, identityDirect{srv: idSrv}),
		org:      org.ID,
		owner:    as(ownerID, "user", org.ID),
		admin:    as(adminID, "user", org.ID),
		member:   as(memberID, "user", org.ID),
		outsider: as(user("outsider"), "user", uuid.New()),
		agent:    authz.WithScope(ctx, authz.Scope{OrgID: org.ID, ActorKind: "agent"}),
		none:     ctx,
	}
}

func code(err error) codes.Code { return status.Code(err) }

// TestServerApprovalLifecycle walks one server from request to revocation and
// pins who may move it at each step: any member asks, only an owner or admin
// decides, and a decision is recorded with who made it and why.
func TestServerApprovalLifecycle(t *testing.T) {
	f := newRegistryFixture(t)

	req, err := f.reg.RequestServer(f.member, &mcpv1.RequestServerRequest{
		Name: "jira", Url: "https://mcp.example.com/jira", Transport: "streamable_http", Description: "Ticket lookup",
	})
	if err != nil {
		t.Fatalf("RequestServer: %v", err)
	}
	s := req.GetServer()
	if s.GetStatus() != "pending" || s.GetOrgId() != f.org.String() || s.GetRequestedBy() == "" {
		t.Fatalf("requested server = %+v", s)
	}

	// The name is unique within the organization.
	if _, err := f.reg.RequestServer(f.admin, &mcpv1.RequestServerRequest{
		Name: "jira", Url: "https://other.example.com", Transport: "streamable_http",
	}); code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate name: %v, want AlreadyExists", err)
	}

	// A plain member cannot decide, including on their own request.
	if _, err := f.reg.DecideServer(f.member, &mcpv1.DecideServerRequest{Id: s.GetId(), Approve: true}); code(err) != codes.PermissionDenied {
		t.Fatalf("member decides: %v, want PermissionDenied", err)
	}
	memberList, err := f.reg.ListServers(f.member, &mcpv1.ListServersRequest{})
	if err != nil {
		t.Fatalf("ListServers as member: %v", err)
	}
	if memberList.GetCanDecide() {
		t.Fatal("a plain member is told they can decide")
	}

	// Rejecting needs a reason; the refusal is the caller's, not a fault.
	if _, err := f.reg.DecideServer(f.admin, &mcpv1.DecideServerRequest{Id: s.GetId(), Approve: false}); code(err) != codes.InvalidArgument {
		t.Fatalf("reject without reason: %v, want InvalidArgument", err)
	}

	dec, err := f.reg.DecideServer(f.admin, &mcpv1.DecideServerRequest{Id: s.GetId(), Approve: true, Reason: "read-only scope"})
	if err != nil {
		t.Fatalf("DecideServer approve: %v", err)
	}
	if got := dec.GetServer(); got.GetStatus() != "approved" || got.GetDecidedBy() == "" || got.GetDecidedAt() == "" || got.GetReason() != "read-only scope" {
		t.Fatalf("approved server = %+v", got)
	}

	// A decision is final: it cannot be decided again.
	if _, err := f.reg.DecideServer(f.owner, &mcpv1.DecideServerRequest{Id: s.GetId(), Approve: false, Reason: "changed my mind"}); code(err) != codes.FailedPrecondition {
		t.Fatalf("second decision: %v, want FailedPrecondition", err)
	}

	approved, err := f.reg.ListServers(f.owner, &mcpv1.ListServersRequest{Status: "approved"})
	if err != nil {
		t.Fatalf("ListServers approved: %v", err)
	}
	if len(approved.GetServers()) != 1 || approved.GetServers()[0].GetName() != "jira" || !approved.GetCanDecide() {
		t.Fatalf("approved list = %+v can_decide=%v", approved.GetServers(), approved.GetCanDecide())
	}

	if _, err := f.reg.RevokeServer(f.member, &mcpv1.RevokeServerRequest{Id: s.GetId()}); code(err) != codes.PermissionDenied {
		t.Fatalf("member revokes: %v, want PermissionDenied", err)
	}
	rev, err := f.reg.RevokeServer(f.owner, &mcpv1.RevokeServerRequest{Id: s.GetId(), Reason: "vendor breach"})
	if err != nil {
		t.Fatalf("RevokeServer: %v", err)
	}
	if rev.GetServer().GetStatus() != "revoked" || rev.GetServer().GetReason() != "vendor breach" {
		t.Fatalf("revoked server = %+v", rev.GetServer())
	}
	if _, err := f.reg.RevokeServer(f.owner, &mcpv1.RevokeServerRequest{Id: s.GetId()}); code(err) != codes.FailedPrecondition {
		t.Fatalf("revoking a revoked server: %v, want FailedPrecondition", err)
	}
	none, err := f.reg.ListServers(f.owner, &mcpv1.ListServersRequest{Status: "approved"})
	if err != nil || len(none.GetServers()) != 0 {
		t.Fatalf("approved after revoke = %+v (%v)", none.GetServers(), err)
	}
}

// TestServerRegistryIsOrgScoped: another organization neither sees nor
// decides this one's servers, whatever id it names.
func TestServerRegistryIsOrgScoped(t *testing.T) {
	f := newRegistryFixture(t)
	req, err := f.reg.RequestServer(f.member, &mcpv1.RequestServerRequest{
		Name: "k8s", Url: "https://k8s.example.com/mcp", Transport: "streamable_http",
	})
	if err != nil {
		t.Fatalf("RequestServer: %v", err)
	}
	list, err := f.reg.ListServers(f.outsider, &mcpv1.ListServersRequest{})
	if err != nil {
		t.Fatalf("ListServers as outsider: %v", err)
	}
	for _, s := range list.GetServers() {
		if s.GetId() == req.GetServer().GetId() {
			t.Fatal("another organization lists this organization's server")
		}
	}
	// The outsider is not a member here, so identity refuses the role lookup
	// before any row is touched.
	if _, err := f.reg.DecideServer(f.outsider, &mcpv1.DecideServerRequest{Id: req.GetServer().GetId(), Approve: true}); code(err) != codes.PermissionDenied && code(err) != codes.NotFound {
		t.Fatalf("outsider decides: %v", err)
	}
	after, err := f.reg.ListServers(f.owner, &mcpv1.ListServersRequest{Status: "pending"})
	if err != nil || len(after.GetServers()) != 1 {
		t.Fatalf("pending after outsider's attempt = %+v (%v)", after.GetServers(), err)
	}
}

// TestRequestServerValidation pins what is refused at the door.
func TestRequestServerValidation(t *testing.T) {
	f := newRegistryFixture(t)
	cases := []struct {
		name string
		ctx  context.Context
		req  *mcpv1.RequestServerRequest
		want codes.Code
	}{
		{"no scope", f.none, &mcpv1.RequestServerRequest{Name: "a", Url: "https://a.example.com", Transport: "streamable_http"}, codes.PermissionDenied},
		{"an agent", f.agent, &mcpv1.RequestServerRequest{Name: "a", Url: "https://a.example.com", Transport: "streamable_http"}, codes.PermissionDenied},
		{"unknown transport", f.member, &mcpv1.RequestServerRequest{Name: "a", Url: "https://a.example.com", Transport: "sse"}, codes.InvalidArgument},
		{"http transport without an http url", f.member, &mcpv1.RequestServerRequest{Name: "a", Url: "file:///etc/passwd", Transport: "streamable_http"}, codes.InvalidArgument},
		{"stdio without a command", f.member, &mcpv1.RequestServerRequest{Name: "a", Url: " ", Transport: "stdio"}, codes.InvalidArgument},
		{"a name that is not a name", f.member, &mcpv1.RequestServerRequest{Name: "Bad Name!", Url: "https://a.example.com", Transport: "streamable_http"}, codes.InvalidArgument},
		{"unknown status filter", f.member, nil, codes.InvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.req == nil {
				_, err = f.reg.ListServers(tc.ctx, &mcpv1.ListServersRequest{Status: "maybe"})
			} else {
				_, err = f.reg.RequestServer(tc.ctx, tc.req)
			}
			if code(err) != tc.want {
				t.Fatalf("code = %v (%v), want %v", code(err), err, tc.want)
			}
		})
	}
	// stdio carries a command line, which is what the transport launches.
	if _, err := f.reg.RequestServer(f.member, &mcpv1.RequestServerRequest{Name: "db", Url: "npx @example/db-mcp", Transport: "stdio"}); err != nil {
		t.Fatalf("stdio request: %v", err)
	}
}
