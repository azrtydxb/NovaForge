package reviews_test

import (
	"context"
	"net"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gates"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/reviews"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// crossForkPool migrates the reviews, gitplatform and gates schemas into a
// throwaway PostgreSQL database and returns a pool on it.
//
// The cluster's shared dev database cannot be used here: it is shared with every
// other lane building against this cluster, and its migration tracking tables
// record whatever version another lane pushed last, at which point golang-migrate
// refuses the whole set ("no migration found for version N") for a reason that
// has nothing to do with the code under test. internal/webhooks/store_test.go
// does the same, and says the same.
func crossForkPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin := dbURL(t)
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect to create a test database: %v", err)
	}
	defer owner.Close(ctx)

	name := "crossfork_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := owner.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		// A connection of its own: dropping a database cannot be done from a
		// connection to it, and the pool below is closed by its own cleanup.
		dropCtx := context.Background()
		conn, err := pgx.Connect(dropCtx, admin)
		if err != nil {
			return
		}
		defer conn.Close(dropCtx)
		_, _ = conn.Exec(dropCtx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	})

	u, err := neturl.Parse(admin)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	u.Path = "/" + name
	if err := database.Migrate(u.String(), "reviews", reviews.MigrationsFS); err != nil {
		t.Fatalf("migrate reviews schema: %v", err)
	}
	if err := database.MigrateAs(u.String(), "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		t.Fatalf("migrate gitplatform schema: %v", err)
	}
	if err := database.Migrate(u.String(), "gates", gates.MigrationsFS); err != nil {
		t.Fatalf("migrate gates schema: %v", err)
	}
	pool, err := database.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestCrossForkRun proves that a run whose source is a fork and whose target is
// the parent goes through exactly the authority a branch run goes through: the
// real gate controller and the independent-approval rule, in that order, with
// the merge refused whenever either is unsatisfied. The services are the real
// ones behind real signed RPCs — a stubbed gate checker here would prove only
// that the stub was called, and the defect this guards against is a cross-fork
// merge that quietly reaches Git by another route.
//
// The last case is the regression guard for the default: an ordinary
// same-repository run in the same parent must still merge, because every run
// that existed before forks has no source repository of its own.
func TestCrossForkRun(t *testing.T) {
	pool := crossForkPool(t)
	const key = "cross-fork-test"
	serve := func(register func(*grpc.Server)) *grpc.ClientConn {
		t.Helper()
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(nil, key)))
		register(srv)
		go srv.Serve(l)
		t.Cleanup(srv.Stop)
		conn, err := grpc.NewClient(l.Addr().String(),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
				// A service call made while handling another service's call
				// carries that call's credential onward, as it does in
				// production where each hop is signed.
				if _, ok := metadata.FromOutgoingContext(ctx); !ok {
					if md, ok := metadata.FromIncomingContext(ctx); ok {
						ctx = metadata.NewOutgoingContext(ctx, md.Copy())
					}
				}
				return invoke(ctx, method, req, reply, cc, opts...)
			}))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}

	org := uuid.New()
	token, err := svcauth.Mint(key, "work-reviews", org, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token)), 90*time.Second)
	defer cancel()

	gitconn := serve(func(s *grpc.Server) { gitv1.RegisterGitServiceServer(s, gitops.NewGRPCServer(pool, t.TempDir())) })
	git := gitv1.NewGitServiceClient(gitconn)
	store := reviews.NewStore(pool)
	reviewServer := reviews.NewGRPCServer(store)
	reviewServer.Git = git
	reviewconn := serve(func(s *grpc.Server) { reviewsv1.RegisterReviewsServiceServer(s, reviewServer) })
	rv := reviewsv1.NewReviewsServiceClient(reviewconn)

	config := &rest.Config{Host: "http://127.0.0.1:1", Timeout: time.Second}
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := gates.NewAnalysisSandbox(kube, config, "analysis@sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	controller := gates.NewController(gates.NewStore(pool), git, rv, nil, "",
		gates.WithAnalysisSandbox(sandbox),
		gates.WithProofContext(func(ctx context.Context) (context.Context, error) {
			scope, err := authz.FromContext(ctx)
			if err != nil {
				return nil, err
			}
			tok, err := svcauth.Mint(key, "gates", scope.OrgID, time.Minute)
			return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+tok)), err
		}))
	gateconn := serve(func(s *grpc.Server) {
		gatesv1.RegisterGatesServiceServer(s, gates.NewGRPCServer(controller, nil, nil, nil))
	})
	gc := gatesv1.NewGatesServiceClient(gateconn)
	reviewServer.Merger = &reviews.Merger{Store: store, Git: git, Gates: reviews.GatesClient{Gates: gc}}

	parent, err := git.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: "upstream-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatal(err)
	}
	commit := func(repo, branch, path, content string) string {
		t.Helper()
		r, e := git.CreateCommit(ctx, &gitv1.CreateCommitRequest{
			Repo: repo, Branch: branch, Message: "change " + path,
			Files: []*gitv1.FileChange{{Path: path, Content: []byte(content)}},
		})
		if e != nil {
			t.Fatalf("commit %s on %s: %v", path, branch, e)
		}
		return r.GetSha()
	}
	head := func(repo, ref string) string {
		t.Helper()
		c, e := git.ListCommits(ctx, &gitv1.ListCommitsRequest{Repo: repo, Ref: ref, Limit: 1})
		if e != nil || len(c.GetCommits()) != 1 {
			t.Fatalf("head of %s in %s: %v %v", ref, repo, c, e)
		}
		return c.GetCommits()[0].GetSha()
	}

	base := commit(parent.GetRepo().GetId(), "main", "README.md", "upstream\n")
	fork, err := git.ForkRepo(ctx, &gitv1.ForkRepoRequest{Repo: parent.GetRepo().GetId(), Name: "fork-" + uuid.NewString()[:8]})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if _, err := git.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: fork.GetRepo().GetId(), Name: "contribution", FromRef: "main"}); err != nil {
		t.Fatal(err)
	}
	forkHead := commit(fork.GetRepo().GetId(), "contribution", "contribution.txt", "from the fork\n")

	run, err := rv.CreateRun(ctx, &reviewsv1.CreateRunRequest{
		RepoId: parent.GetRepo().GetId(), SourceRepoId: fork.GetRepo().GetId(),
		Title: "cross-fork proposal", SourceRef: "contribution", TargetRef: "main",
		AuthorId: uuid.NewString(), AuthorKind: "user",
	})
	if err != nil {
		t.Fatalf("create cross-fork run: %v", err)
	}
	if got := run.GetRun().GetSourceRepoId(); got != fork.GetRepo().GetId() {
		t.Fatalf("run source repository = %q, want the fork %q", got, fork.GetRepo().GetId())
	}

	// The approval rule first: a cross-fork run with no independent approval is
	// refused, and the parent's branch does not move.
	if _, err := rv.MergeRun(ctx, &reviewsv1.MergeRunRequest{RunId: run.GetRun().GetId(), Method: "merge"}); err == nil {
		t.Fatal("cross-fork run merged with no independent approval")
	}
	if got := head(parent.GetRepo().GetId(), "main"); got != base {
		t.Fatalf("parent main moved to %s on a refused merge, want %s", got, base)
	}

	scoped := scopedCtx(org)
	runID := uuid.MustParse(run.GetRun().GetId())
	if err := store.SubmitReviewAt(scoped, runID, uuid.New(), "user", "approve", forkHead, "inspected the fork's branch"); err != nil {
		t.Fatalf("SubmitReviewAt: %v", err)
	}
	merged, err := rv.MergeRun(ctx, &reviewsv1.MergeRunRequest{RunId: run.GetRun().GetId(), Method: "merge"})
	if err != nil || merged.GetMergeSha() == "" {
		t.Fatalf("cross-fork merge: %v %v", merged, err)
	}
	if got := head(parent.GetRepo().GetId(), "main"); got != merged.GetMergeSha() {
		t.Fatalf("parent main = %s, want the merge commit %s", got, merged.GetMergeSha())
	}
	blob, err := git.GetBlob(ctx, &gitv1.GetBlobRequest{Repo: parent.GetRepo().GetId(), Ref: "main", Path: "contribution.txt"})
	if err != nil || string(blob.GetContent()) != "from the fork\n" {
		t.Fatalf("parent main does not carry the fork's change: %v %v", blob, err)
	}
	// Merging out of a fork must not write to it.
	if got := head(fork.GetRepo().GetId(), "contribution"); got != forkHead {
		t.Fatalf("fork's branch moved to %s during the merge, want %s", got, forkHead)
	}
	if got := head(fork.GetRepo().GetId(), "main"); got != base {
		t.Fatalf("fork's main moved to %s during the merge, want %s", got, base)
	}

	// Now the gate authority, on a second cross-fork run: the parent declares a
	// required gate, so the merge must be refused until that gate is satisfied.
	// This fixture deliberately has no reachable analysis sandbox, so the tests
	// gate cannot execute. The separate TestCrossForkExecutableGate uses a real
	// pod and proves a passing fork can merge. The refusal here must still name
	// the parent's definition rather than an unrelated checkout failure.
	commit(parent.GetRepo().GetId(), "main", ".novaforge/gates/tests.yaml", "name: tests\nrequired: true\n")
	guarded := head(parent.GetRepo().GetId(), "main")
	if _, err := git.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: fork.GetRepo().GetId(), Name: "second", FromRef: "main"}); err != nil {
		t.Fatal(err)
	}
	secondHead := commit(fork.GetRepo().GetId(), "second", "second.txt", "more work\n")
	second, err := rv.CreateRun(ctx, &reviewsv1.CreateRunRequest{
		RepoId: parent.GetRepo().GetId(), SourceRepoId: fork.GetRepo().GetId(),
		Title: "gated cross-fork proposal", SourceRef: "second", TargetRef: "main",
		AuthorId: uuid.NewString(), AuthorKind: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SubmitReviewAt(scoped, uuid.MustParse(second.GetRun().GetId()), uuid.New(), "user", "approve", secondHead, "inspected"); err != nil {
		t.Fatal(err)
	}
	_, err = rv.MergeRun(ctx, &reviewsv1.MergeRunRequest{RunId: second.GetRun().GetId(), Method: "merge"})
	if err == nil {
		t.Fatal("cross-fork run merged while the parent's required gate was unsatisfied")
	}
	// Refusal alone does not prove the parent's authority: a cross-fork run is
	// refused for several reasons, and "the gates could not be read at all" looks
	// identical from the outside to "the parent's gate was not satisfied". The
	// refusal must therefore name the gate the PARENT declared, which is only
	// possible if that definition was resolved and applied.
	//
	// Reading definitions from the fork instead was tried, by making the lookup
	// report the fork as the run's repository. The merge was still refused — but
	// with "unknown ref or path", the parent's gate never resolved at all. That is
	// the failure this assertion distinguishes.
	if !strings.Contains(err.Error(), "tests") {
		t.Fatalf("the refusal does not name the gate the parent declared, so the parent's definition was never applied: %v", err)
	}
	if got := head(parent.GetRepo().GetId(), "main"); got != guarded {
		t.Fatalf("parent main moved to %s while a required gate was unsatisfied, want %s", got, guarded)
	}

	// The default: a run that names no source repository is a run in its own
	// repository, exactly as every run created before forks existed.
	if _, err := git.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: parent.GetRepo().GetId(), Name: "in-tree", FromRef: "main"}); err != nil {
		t.Fatal(err)
	}
	// The required gate declared above would block this one too, so its
	// definition is deleted first: this case is about the source defaulting, not
	// about gates. It has to be deleted rather than emptied — an empty
	// definition file is a malformed one, which the resolver rejects outright.
	if _, err := git.CreateCommit(ctx, &gitv1.CreateCommitRequest{
		Repo: parent.GetRepo().GetId(), Branch: "main", Message: "drop the gate definition",
		Files: []*gitv1.FileChange{{Path: ".novaforge/gates/tests.yaml", Deleted: true}},
	}); err != nil {
		t.Fatal(err)
	}
	inTreeHead := commit(parent.GetRepo().GetId(), "in-tree", "in-tree.txt", "branch work\n")
	branchRun, err := rv.CreateRun(ctx, &reviewsv1.CreateRunRequest{
		RepoId: parent.GetRepo().GetId(), Title: "ordinary branch run",
		SourceRef: "in-tree", TargetRef: "main",
		AuthorId: uuid.NewString(), AuthorKind: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := branchRun.GetRun().GetSourceRepoId(); got != parent.GetRepo().GetId() {
		t.Fatalf("a run with no source repository reports %q, want its own repository %q", got, parent.GetRepo().GetId())
	}
	if err := store.SubmitReviewAt(scoped, uuid.MustParse(branchRun.GetRun().GetId()), uuid.New(), "user", "approve", inTreeHead, "inspected"); err != nil {
		t.Fatal(err)
	}
	branchMerged, err := rv.MergeRun(ctx, &reviewsv1.MergeRunRequest{RunId: branchRun.GetRun().GetId(), Method: "merge"})
	if err != nil || branchMerged.GetMergeSha() == "" {
		t.Fatalf("ordinary branch run no longer merges: %v %v", branchMerged, err)
	}
	if got := head(parent.GetRepo().GetId(), "main"); got != branchMerged.GetMergeSha() {
		t.Fatalf("parent main = %s, want the branch run's merge %s", got, branchMerged.GetMergeSha())
	}
}
