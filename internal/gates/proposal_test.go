package gates_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gates"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/reviews"
)

// gitDirect presents a real gitops.Server as a GitServiceClient, calling it in
// process so the caller's authz.Scope reaches it exactly as the interceptor
// would install it. The server is the production one over real git and real
// PostgreSQL; only the transport is skipped. A method the proposal path does
// not use panics through the nil embedded interface, which is loud rather
// than silently answered.
type gitDirect struct {
	gitv1.GitServiceClient
	srv *gitops.Server
}

func (g gitDirect) GetRepo(ctx context.Context, in *gitv1.GetRepoRequest, _ ...grpc.CallOption) (*gitv1.GetRepoResponse, error) {
	return g.srv.GetRepo(ctx, in)
}
func (g gitDirect) ListBranches(ctx context.Context, in *gitv1.ListBranchesRequest, _ ...grpc.CallOption) (*gitv1.ListBranchesResponse, error) {
	return g.srv.ListBranches(ctx, in)
}
func (g gitDirect) GetTree(ctx context.Context, in *gitv1.GetTreeRequest, _ ...grpc.CallOption) (*gitv1.GetTreeResponse, error) {
	return g.srv.GetTree(ctx, in)
}
func (g gitDirect) GetBlob(ctx context.Context, in *gitv1.GetBlobRequest, _ ...grpc.CallOption) (*gitv1.GetBlobResponse, error) {
	return g.srv.GetBlob(ctx, in)
}
func (g gitDirect) CreateBranch(ctx context.Context, in *gitv1.CreateBranchRequest, _ ...grpc.CallOption) (*gitv1.CreateBranchResponse, error) {
	return g.srv.CreateBranch(ctx, in)
}
func (g gitDirect) CreateCommit(ctx context.Context, in *gitv1.CreateCommitRequest, _ ...grpc.CallOption) (*gitv1.CreateCommitResponse, error) {
	return g.srv.CreateCommit(ctx, in)
}

type reviewsDirect struct {
	reviewsv1.ReviewsServiceClient
	srv *reviews.GRPCServer
}

func (r reviewsDirect) CreateRun(ctx context.Context, in *reviewsv1.CreateRunRequest, _ ...grpc.CallOption) (*reviewsv1.CreateRunResponse, error) {
	return r.srv.CreateRun(ctx, in)
}

type proposalFixture struct {
	gates *gates.GRPCServer
	git   *gitops.Server
	runs  *reviews.GRPCServer
	org   uuid.UUID
	repo  string
	ctx   context.Context
}

// newProposalFixture builds a repository whose default branch declares the
// documentation gate as required, with a comment and params that a toggle
// must not lose.
func newProposalFixture(t *testing.T) proposalFixture {
	t.Helper()
	url := dbURL(t)
	if err := database.MigrateAs(url, "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform: %v", err)
	}
	if err := database.Migrate(url, "reviews", reviews.MigrationsFS); err != nil {
		t.Fatalf("migrate reviews: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	root := t.TempDir()
	gitSrv := gitops.NewGRPCServer(pool, root)
	runSrv := reviews.NewGRPCServer(reviews.NewStore(pool))

	org := uuid.New()
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: org, ActorID: uuid.New(), ActorKind: "user"})
	repo := "gates-" + uuid.NewString()[:8]
	if _, err := gitSrv.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: repo}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	seedRepo(t, filepath.Join(root, org.String(), repo+".git"), map[string]string{
		".novaforge/gates/docs.yaml": "# Public packages must stay documented.\nname: documentation\nrequired: true\nparams:\n  min_coverage: 80\n",
		"README.md":                  "hello\n",
	})

	srv := gates.NewGRPCServer(nil, nil, nil, nil)
	srv.Proposals = &gates.Proposer{
		Git:     gitDirect{srv: gitSrv},
		Reviews: reviewsDirect{srv: runSrv},
	}
	return proposalFixture{gates: srv, git: gitSrv, runs: runSrv, org: org, repo: repo, ctx: ctx}
}

func seedRepo(t *testing.T, bare string, files map[string]string) {
	t.Helper()
	work := t.TempDir()
	gitCmd(t, "", "clone", bare, work)
	for path, body := range files {
		full := filepath.Join(work, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd(t, work, "add", "-A")
	gitCmd(t, work, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-m", "seed")
	gitCmd(t, work, "push", "origin", "HEAD:main")
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (f proposalFixture) blob(t *testing.T, ref, path string) string {
	t.Helper()
	b, err := f.git.GetBlob(f.ctx, &gitv1.GetBlobRequest{Repo: f.repo, Ref: ref, Path: path})
	if err != nil {
		t.Fatalf("GetBlob %s@%s: %v", path, ref, err)
	}
	return string(b.GetContent())
}

// TestProposeGateChangeOpensARunAndLeavesMainAlone is the property the
// feature exists for: turning a gate off writes nothing to the default
// branch. The change lands on its own branch, as a run that must be reviewed
// and merged like any other — and that run's gates are resolved from main,
// where the gate is still on.
func TestProposeGateChangeOpensARunAndLeavesMainAlone(t *testing.T) {
	f := newProposalFixture(t)
	before := f.blob(t, "main", ".novaforge/gates/docs.yaml")

	resp, err := f.gates.ProposeGateChange(f.ctx, &gatesv1.ProposeGateChangeRequest{
		Repo: f.repo, Gate: "documentation", Enabled: proto.Bool(false),
	})
	if err != nil {
		t.Fatalf("ProposeGateChange: %v", err)
	}
	if resp.GetRunNumber() <= 0 {
		t.Fatalf("want an allocated run number, got %d", resp.GetRunNumber())
	}
	if !strings.HasPrefix(resp.GetBranch(), "gates/documentation-") {
		t.Fatalf("branch = %q, want gates/documentation-<random>", resp.GetBranch())
	}

	if after := f.blob(t, "main", ".novaforge/gates/docs.yaml"); after != before {
		t.Fatalf("the default branch changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	proposed := f.blob(t, resp.GetBranch(), ".novaforge/gates/docs.yaml")
	if !strings.Contains(proposed, "required: false") {
		t.Fatalf("proposed file does not disable the gate:\n%s", proposed)
	}
	// Rewriting the file must not drop what the author put in it.
	if !strings.Contains(proposed, "# Public packages must stay documented.") || !strings.Contains(proposed, "min_coverage: 80") {
		t.Fatalf("proposed file lost the comment or params:\n%s", proposed)
	}

	run, err := f.runs.GetRun(f.ctx, &reviewsv1.GetRunRequest{Id: resp.GetRunId()})
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	got := run.GetRun()
	if got.GetNumber() != resp.GetRunNumber() || got.GetSourceRef() != resp.GetBranch() || got.GetTargetRef() != "main" {
		t.Fatalf("run = #%d %s -> %s, want #%d %s -> main", got.GetNumber(), got.GetSourceRef(), got.GetTargetRef(), resp.GetRunNumber(), resp.GetBranch())
	}
	if got.GetTitle() != "Disable the documentation gate" {
		t.Fatalf("title = %q", got.GetTitle())
	}
	if got.GetAuthorKind() != "user" {
		t.Fatalf("author_kind = %q, want user", got.GetAuthorKind())
	}

	// ListGateConfig still reads main, where the gate is on.
	list, err := f.gates.ListGateConfig(f.ctx, &gatesv1.ListGateConfigRequest{Repo: f.repo})
	if err != nil {
		t.Fatalf("ListGateConfig: %v", err)
	}
	var docs *gatesv1.GateConfig
	for _, g := range list.GetGates() {
		if g.GetName() == "documentation" {
			docs = g
		}
	}
	if docs == nil || !docs.GetDeclared() || !docs.GetEnabled() || docs.GetPath() != ".novaforge/gates/docs.yaml" {
		t.Fatalf("documentation gate on main = %+v, want declared and enabled", docs)
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(docs.GetParamsJson()), &params); err != nil || params["min_coverage"] != float64(80) {
		t.Fatalf("params_json = %q (%v)", docs.GetParamsJson(), err)
	}
	if len(list.GetGates()) != 7 {
		t.Fatalf("want every known gate listed, got %d", len(list.GetGates()))
	}
}

// TestProposeEnablingAnUndeclaredGateCreatesItsFile covers the other
// direction: a gate with no file gets one, on the proposal branch.
func TestProposeEnablingAnUndeclaredGateCreatesItsFile(t *testing.T) {
	f := newProposalFixture(t)
	resp, err := f.gates.ProposeGateChange(f.ctx, &gatesv1.ProposeGateChangeRequest{
		Repo: f.repo, Gate: "security", Enabled: proto.Bool(true), ParamsJson: `{"severity":"high"}`,
	})
	if err != nil {
		t.Fatalf("ProposeGateChange: %v", err)
	}
	body := f.blob(t, resp.GetBranch(), ".novaforge/gates/security.yaml")
	for _, want := range []string{"name: security", "required: true", "severity: high"} {
		if !strings.Contains(body, want) {
			t.Fatalf("new gate file missing %q:\n%s", want, body)
		}
	}
	if resp.GetTitle() != "Enable the security gate" {
		t.Fatalf("title = %q", resp.GetTitle())
	}
	if _, err := f.git.GetBlob(f.ctx, &gitv1.GetBlobRequest{Repo: f.repo, Ref: "main", Path: ".novaforge/gates/security.yaml"}); status.Code(err) != codes.NotFound {
		t.Fatalf("the gate file reached main: %v", err)
	}
}

// TestGateConfigOfAnEmptyRepository: a repository with no commits declares
// no gates, which the settings screen must be able to show rather than fail
// on — every repository starts that way. Proposing against it is refused.
func TestGateConfigOfAnEmptyRepository(t *testing.T) {
	f := newProposalFixture(t)
	empty := "empty-" + uuid.NewString()[:8]
	if _, err := f.git.CreateRepo(f.ctx, &gitv1.CreateRepoRequest{Name: empty}); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	list, err := f.gates.ListGateConfig(f.ctx, &gatesv1.ListGateConfigRequest{Repo: empty})
	if err != nil {
		t.Fatalf("ListGateConfig: %v", err)
	}
	for _, g := range list.GetGates() {
		if g.GetDeclared() || g.GetEnabled() {
			t.Fatalf("an empty repository declares %+v", g)
		}
	}
	_, err = f.gates.ProposeGateChange(f.ctx, &gatesv1.ProposeGateChangeRequest{Repo: empty, Gate: "tests", Enabled: proto.Bool(true)})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("propose on an empty repository: %v, want FailedPrecondition", err)
	}
}

// TestProposeGateChangeRefusals pins what must not become a run.
func TestProposeGateChangeRefusals(t *testing.T) {
	f := newProposalFixture(t)

	cases := []struct {
		name string
		ctx  context.Context
		req  *gatesv1.ProposeGateChangeRequest
		want codes.Code
	}{
		{"a change that changes nothing", f.ctx,
			&gatesv1.ProposeGateChangeRequest{Repo: f.repo, Gate: "documentation", Enabled: proto.Bool(true)}, codes.FailedPrecondition},
		{"no change asked for", f.ctx,
			&gatesv1.ProposeGateChangeRequest{Repo: f.repo, Gate: "documentation"}, codes.InvalidArgument},
		{"a gate the platform does not have", f.ctx,
			&gatesv1.ProposeGateChangeRequest{Repo: f.repo, Gate: "vibes", Enabled: proto.Bool(false)}, codes.InvalidArgument},
		{"params that are not an object", f.ctx,
			&gatesv1.ProposeGateChangeRequest{Repo: f.repo, Gate: "documentation", ParamsJson: `[1]`}, codes.InvalidArgument},
		{"an agent", authz.WithScope(context.Background(), authz.Scope{OrgID: f.org, ActorID: uuid.New(), ActorKind: "agent"}),
			&gatesv1.ProposeGateChangeRequest{Repo: f.repo, Gate: "documentation", Enabled: proto.Bool(false)}, codes.PermissionDenied},
		{"no scope at all", context.Background(),
			&gatesv1.ProposeGateChangeRequest{Repo: f.repo, Gate: "documentation", Enabled: proto.Bool(false)}, codes.PermissionDenied},
		// Another organization cannot name this repository and be believed:
		// git-platform resolves the name inside the caller's own org.
		{"another organization", authz.WithScope(context.Background(), authz.Scope{OrgID: uuid.New(), ActorID: uuid.New(), ActorKind: "user"}),
			&gatesv1.ProposeGateChangeRequest{Repo: f.repo, Gate: "documentation", Enabled: proto.Bool(false)}, codes.NotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.gates.ProposeGateChange(tc.ctx, tc.req)
			if status.Code(err) != tc.want {
				t.Fatalf("code = %v (%v), want %v", status.Code(err), err, tc.want)
			}
		})
	}

	branches, err := f.git.ListBranches(f.ctx, &gitv1.ListBranchesRequest{Repo: f.repo})
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	for _, b := range branches.GetRefs() {
		if strings.HasPrefix(b.GetName(), "gates/") {
			t.Fatalf("a refused proposal left branch %q behind", b.GetName())
		}
	}
}
