package edge_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/edge"
	"github.com/novaforge/novaforge/internal/work"
)

// These tests put the edge in front of the real services — real stores on
// the real database, over a real gRPC listener — because a field the edge
// accepts and a service drops, or a service returns and the edge never
// renders, is invisible to a test of either side alone.

func liveDB(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	p, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p, url
}

// migrateLive applies a schema's migrations, tolerating only a shared dev
// database that is ahead of this checkout (see internal/cleanup's tests).
func migrateLive(t *testing.T, url, schema, tracking string, fsys fs.FS) {
	t.Helper()
	var err error
	if tracking == "" {
		err = database.Migrate(url, schema, fsys)
	} else {
		err = database.MigrateAs(url, schema, tracking, fsys)
	}
	if err != nil && !strings.Contains(err.Error(), "no migration found for version") {
		t.Fatalf("migrate %s: %v", schema, err)
	}
}

// serveScoped registers services on a real listener whose every call carries scope,
// standing in for the credential resolution svcauth does in production.
func serveScoped(t *testing.T, scope *authz.Scope, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		return h(authz.WithScope(ctx, *scope), req)
	}))
	register(srv)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func decodeJSON(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

// TestWorkItemLifecycle is spec S-4's criterion through the REST API: a Work
// Item is created with its type, goal, acceptance criteria, constraints and
// required gates, reads back with every one of them, refuses a gate the
// platform does not have, and is assigned to a person and then to an agent.
func TestWorkItemLifecycle(t *testing.T) {
	pool, url := liveDB(t)
	migrateLive(t, url, "work", "", work.MigrationsFS)

	orgID, person, agent := uuid.New(), uuid.New(), uuid.New()
	repoID := uuid.New()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM work.work_items WHERE org_id = $1`, orgID)
	})
	scope := authz.Scope{OrgID: orgID, ActorID: person, ActorKind: "user", Role: "owner"}
	conn := serveScoped(t, &scope, func(s *grpc.Server) {
		workv1.RegisterWorkServiceServer(s, work.NewGRPCServer(work.NewStore(pool)))
	})
	git := &gitDouble{repos: map[string]*gitv1.Repo{"platform": {Id: repoID.String(), Name: "platform", DefaultBranch: "main"}}}
	members := &membersDouble{members: []*identityv1.OrgMember{{UserId: person.String(), Username: "pat", Role: "owner"}}}
	agentsSvc := &agentsDouble{agents: []*agentsv1.Agent{{Id: agent.String(), Name: "builder", Role: "implementer", Enabled: true}}}
	h := edge.Handlers(edge.Config{Git: git, Work: workv1.NewWorkServiceClient(conn), Identity: members, Agents: agentsSvc})
	params := map[string]string{"org": "acme", "repo": "platform"}

	rec := call(t, h, "createWorkItem", http.MethodPost, `{
		"type": "feature",
		"goal": "Rate-limit the public API",
		"acceptance": ["429 after 100 requests a minute", "limits are per token"],
		"constraints": ["no new datastore", "p99 latency unchanged"],
		"required_gates": ["tests", "security"]
	}`, params)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Key string `json:"key"`
	}
	decodeJSON(t, rec.Body.Bytes(), &created)

	type item struct {
		Type          string   `json:"type"`
		Goal          string   `json:"goal"`
		Acceptance    []string `json:"acceptance"`
		Constraints   []string `json:"constraints"`
		RequiredGates []string `json:"required_gates"`
		AssigneeID    string   `json:"assignee_id"`
		AssigneeKind  string   `json:"assignee_kind"`
	}
	read := func() item {
		t.Helper()
		rec := call(t, h, "getWorkItem", http.MethodGet, "", map[string]string{"org": "acme", "repo": "platform", "key": created.Key})
		if rec.Code != http.StatusOK {
			t.Fatalf("get: status %d, body %s", rec.Code, rec.Body.String())
		}
		var got item
		decodeJSON(t, rec.Body.Bytes(), &got)
		return got
	}
	got := read()
	if got.Type != "feature" || got.Goal != "Rate-limit the public API" {
		t.Fatalf("type/goal read back as %q / %q", got.Type, got.Goal)
	}
	if strings.Join(got.Acceptance, "|") != "429 after 100 requests a minute|limits are per token" {
		t.Fatalf("acceptance read back as %q", got.Acceptance)
	}
	if strings.Join(got.Constraints, "|") != "no new datastore|p99 latency unchanged" {
		t.Fatalf("constraints read back as %q", got.Constraints)
	}
	if strings.Join(got.RequiredGates, "|") != "tests|security" {
		t.Fatalf("required gates read back as %q", got.RequiredGates)
	}

	// A gate the platform does not have could never be satisfied: the item
	// would be unmergeable forever, for a typo.
	rec = call(t, h, "createWorkItem", http.MethodPost, `{"type":"bug","goal":"x","required_gates":["test"]}`, params)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown required gate: status %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}

	assign := func(id, kind string) {
		t.Helper()
		rec := call(t, h, "assignWorkItem", http.MethodPost,
			`{"assignee_id":"`+id+`","assignee_kind":"`+kind+`"}`,
			map[string]string{"org": "acme", "repo": "platform", "key": created.Key})
		if rec.Code != http.StatusOK {
			t.Fatalf("assign to %s: status %d, body %s", kind, rec.Code, rec.Body.String())
		}
	}
	assign(person.String(), "user")
	if got := read(); got.AssigneeID != person.String() || got.AssigneeKind != "user" {
		t.Fatalf("after assigning a person: %s %s", got.AssigneeKind, got.AssigneeID)
	}
	assign(agent.String(), "agent")
	if got := read(); got.AssigneeID != agent.String() || got.AssigneeKind != "agent" {
		t.Fatalf("after assigning an agent: %s %s", got.AssigneeKind, got.AssigneeID)
	}

	// Someone outside the organization, and another organization's agent, are
	// refused: the work service would store any id it was given.
	for _, bad := range []struct{ id, kind string }{
		{uuid.NewString(), "user"}, {uuid.NewString(), "agent"}, {person.String(), "robot"},
	} {
		rec := call(t, h, "assignWorkItem", http.MethodPost,
			`{"assignee_id":"`+bad.id+`","assignee_kind":"`+bad.kind+`"}`,
			map[string]string{"org": "acme", "repo": "platform", "key": created.Key})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("assigning %s %s outside the organization: status %d, want 400", bad.kind, bad.id, rec.Code)
		}
	}
	if got := read(); got.AssigneeID != agent.String() {
		t.Fatalf("a refused assignment changed the assignee to %s", got.AssigneeID)
	}
}

type membersDouble struct {
	identityv1.IdentityServiceClient
	members []*identityv1.OrgMember
}

func (m *membersDouble) ListOrgMembers(_ context.Context, _ *identityv1.ListOrgMembersRequest, _ ...grpc.CallOption) (*identityv1.ListOrgMembersResponse, error) {
	return &identityv1.ListOrgMembersResponse{Members: m.members}, nil
}
