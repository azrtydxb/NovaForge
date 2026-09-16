package maintenance_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/maintenance"
	"github.com/novaforge/novaforge/internal/svcauth"
	"github.com/novaforge/novaforge/internal/work"
)

const sweepSecret = "sweep-test-secret"

// TestMaintenanceProposesWorkItem is spec S-20's criterion through the path
// production runs: a real repository, holding a module pinned to
// golang.org/x/text v0.3.0, served by the real git service over gRPC; the
// sweep production builds (maintenance.NewSweeper) finds the organization,
// reads the repository through that service, runs the real osv-scanner, and
// proposes a Work Item that no agent is assigned to and that waits for a
// person's approval.
//
// The organization has repositories and no Work Item at all. That is exactly
// the organization the sweep used to skip: it found organizations through the
// work schema, so the first vulnerability in a new organization could never
// propose its first Work Item.
func TestMaintenanceProposesWorkItem(t *testing.T) {
	for _, tool := range []string{"go", "osv-scanner"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is not installed; this test runs the real tool (see deploy/docker/Dockerfile.analysis)", tool)
		}
	}
	url := proposeDBURL(t)
	store := proposeWorkStore(t)
	if err := database.MigrateAs(url, "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform: %v", err)
	}
	pool, err := database.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(svcauth.UnaryServerInterceptor(nil, sweepSecret)))
	gitv1.RegisterGitServiceServer(srv, gitops.NewGRPCServer(pool, t.TempDir()))
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	git := gitv1.NewGitServiceClient(conn)

	orgID := uuid.New()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM work.work_items WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM gitplatform.repositories WHERE org_id = $1`, orgID)
	})
	tok, err := svcauth.Mint(sweepSecret, "sweep-test", orgID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	asOrg := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)

	repo, err := git.CreateRepo(asOrg, &gitv1.CreateRepoRequest{Name: "vulnerable-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	files := vulnerableModule(t)
	files = append(files,
		&gitv1.FileChange{Path: ".novaforge/gates/architecture.yaml", Content: []byte("name: architecture\nrequired: true\nparams:\n  forbidden_dependencies:\n    - frontend -> database\n")},
		&gitv1.FileChange{Path: "frontend/frontend.go", Content: []byte("package frontend\nimport _ \"example.com/probe/database\"\n")},
		&gitv1.FileChange{Path: "database/database.go", Content: []byte("package database\n")},
	)
	if _, err := git.CreateCommit(asOrg, &gitv1.CreateCommitRequest{
		Repo: repo.GetRepo().GetName(), Branch: repo.GetRepo().GetDefaultBranch(),
		Message: "add a module with a vulnerable dependency", Files: files,
		AuthorName: "Sweep Test", AuthorEmail: "sweep@example.com",
	}); err != nil {
		t.Fatalf("CreateCommit: %v", err)
	}

	sweeper := maintenance.NewSweeper(store, git, sweepSecret)
	sweeper.CI = historyCI(t, orgID, uuid.MustParse(repo.GetRepo().GetId()))

	// The organization list is production's, unfiltered, and must name this
	// organization. The sweep is then confined to it only because the dev
	// database is shared: every other test's organization is in that list
	// too, and scanning them all proves nothing more.
	orgs, err := sweeper.Orgs(ctx)
	if err != nil {
		t.Fatalf("list organizations: %v", err)
	}
	listed := false
	for _, id := range orgs {
		if id == orgID {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("the sweep's organization list does not include an organization with a repository and no Work Item (%d listed)", len(orgs))
	}
	sweeper.Orgs = func(context.Context) ([]uuid.UUID, error) { return []uuid.UUID{orgID}, nil }

	report := sweeper.Sweep(ctx)
	if report.Repositories != 1 {
		t.Fatalf("sweep scanned %d repositories, want 1 (errors: %v)", report.Repositories, report.Errors)
	}

	scoped := proposeScopedCtx(orgID)
	repoID := uuid.MustParse(repo.GetRepo().GetId())
	open, err := store.OpenProposalFingerprints(scoped, orgID, repoID)
	if err != nil {
		t.Fatalf("OpenProposalFingerprints: %v", err)
	}
	var cve *work.Item
	var architecture *work.Item
	var flaky *work.Item
	for _, itemID := range open {
		item, err := store.Get(scoped, itemID)
		if err != nil {
			t.Fatalf("Get proposal item: %v", err)
		}
		if item.Type == "security" && strings.Contains(item.Goal, "golang.org/x/text") {
			cve = &item
		}
		if item.Type == "tech_debt" && strings.Contains(item.Goal, "TestFlaky is flaky") {
			flaky = &item
		}
		if item.Type == "architecture" && strings.Contains(item.Goal, "forbidden dependency") {
			architecture = &item
		}
	}
	if flaky == nil {
		t.Fatalf("CI test history never produced a flaky-test proposal: %+v", report)
	}
	if architecture == nil {
		t.Fatalf("repository architecture policy never produced a proposal: %+v", report)
	}
	if cve == nil {
		t.Fatalf("no security proposal for golang.org/x/text after the sweep (report %+v)", report)
	}
	if cve.AssigneeID != uuid.Nil || cve.AssigneeKind != "" {
		t.Fatalf("the proposal was assigned to %s %v; nobody may be assigned before a person approves", cve.AssigneeKind, cve.AssigneeID)
	}
	awaiting, err := store.AwaitingApproval(scoped, cve.ID)
	if err != nil {
		t.Fatalf("AwaitingApproval: %v", err)
	}
	if !awaiting {
		t.Fatal("the proposal is not awaiting approval, so a run could be started on it")
	}

	// A malformed policy must not hide CVEs or other independent findings.
	// Exercise both entry points: the person sees ScannerErrors, and the
	// periodic sweep must retain those errors in its report too.
	if _, err := git.CreateCommit(asOrg, &gitv1.CreateCommitRequest{
		Repo: repo.GetRepo().GetName(), Branch: repo.GetRepo().GetDefaultBranch(),
		Message: "break architecture policy", Files: []*gitv1.FileChange{{
			Path: ".novaforge/gates/architecture.yaml", Content: []byte("params: ["),
		}},
		AuthorName: "Sweep Test", AuthorEmail: "sweep@example.com",
	}); err != nil {
		t.Fatalf("CreateCommit malformed policy: %v", err)
	}
	scanCtx := authz.WithScope(asOrg, authz.Scope{OrgID: orgID, ActorKind: "service"})
	result, err := sweeper.Scanner()(scanCtx, orgID, repoID)
	if err != nil {
		t.Fatalf("malformed architecture policy aborted unrelated scanners: %v", err)
	}
	if result.Findings == 0 || !strings.Contains(strings.Join(result.ScannerErrors, "\n"), "architectural_violation:") {
		t.Fatalf("want unrelated findings and an architecture error, got %+v", result)
	}
	report = sweeper.Sweep(ctx)
	if !strings.Contains(strings.Join(report.Errors, "\n"), "architectural_violation:") {
		t.Fatalf("periodic sweep discarded the scanner failure: %+v", report)
	}

	// The organization list answers only a platform worker.
	if _, err := git.ListOrganizationsWithRepositories(asOrg, &gitv1.ListOrganizationsWithRepositoriesRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("an org-scoped caller listed organizations: %v", err)
	}
}

// vulnerableModule builds the files of a module requiring
// golang.org/x/text v0.3.0, with the go.sum go itself writes.
func vulnerableModule(t *testing.T) []*gitv1.FileChange {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/probe\n\ngo 1.22\n\nrequire golang.org/x/text v0.3.0\n")
	write(t, dir, "main.go", "package probe\n\nimport _ \"golang.org/x/text/language\"\n")
	if out, err := exec.Command("go", "-C", dir, "mod", "tidy").CombinedOutput(); err != nil {
		t.Fatalf("tidy: %v: %s", err, out)
	}
	var files []*gitv1.FileChange
	for _, name := range []string{"go.mod", "go.sum", "main.go"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, &gitv1.FileChange{Path: name, Content: body})
	}
	return files
}
