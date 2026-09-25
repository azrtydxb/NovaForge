package reviews_test

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/database"
	"github.com/novaforge/novaforge/internal/gates"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/reviews"
	"github.com/novaforge/novaforge/internal/svcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type movingTargetGates struct {
	gatesv1.GatesServiceClient
	advance atomic.Bool
	move    func()
}

func (g *movingTargetGates) MayMerge(ctx context.Context, req *gatesv1.MayMergeRequest, opts ...grpc.CallOption) (*gatesv1.MayMergeResponse, error) {
	response, err := g.GatesServiceClient.MayMerge(ctx, req, opts...)
	if err == nil && response.GetAllowed() && g.advance.Swap(false) {
		g.move()
	}
	return response, err
}

// Actual Git/Gates/Reviews owners and signed RPCs, with real PostgreSQL and Git.
// API compatibility reads immutable Git blobs and needs no tool process. The
// configured sandbox is real, not a success stub; its unused Kubernetes endpoint
// is intentionally unreachable. This is not cluster sandbox execution evidence.
func TestPinnedMergeAndProofThroughOwners(t *testing.T) {
	pool := storePool(t)
	url := dbURL(t)
	if err := database.MigrateAs(url, "gitplatform", "gitplatform_git", gitops.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(url, "gates", gates.MigrationsFS); err != nil {
		t.Fatal(err)
	}
	const key = "pinned-owner-test"
	serve := func(register func(*grpc.Server)) (*grpc.ClientConn, *grpc.Server) {
		t.Helper()
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := grpc.NewServer(grpc.UnaryInterceptor(svcauth.UnaryServerInterceptor(nil, key)))
		register(srv)
		go srv.Serve(l)
		t.Cleanup(srv.Stop)
		conn, err := grpc.NewClient(l.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
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
		return conn, srv
	}
	org := uuid.New()
	token, err := svcauth.Mint(key, "work-reviews", org, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token)), 30*time.Second)
	defer cancel()
	gitconn, _ := serve(func(s *grpc.Server) { gitv1.RegisterGitServiceServer(s, gitops.NewGRPCServer(pool, t.TempDir())) })
	git := gitv1.NewGitServiceClient(gitconn)
	store := reviews.NewStore(pool)
	reviewServer := reviews.NewGRPCServer(store)
	reviewServer.Git = git
	reviewconn, _ := serve(func(s *grpc.Server) { reviewsv1.RegisterReviewsServiceServer(s, reviewServer) })
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
	controller := gates.NewController(gates.NewStore(pool), git, rv, nil, "", gates.WithAnalysisSandbox(sandbox), gates.WithProofContext(func(ctx context.Context) (context.Context, error) {
		scope, err := authz.FromContext(ctx)
		if err != nil {
			return nil, err
		}
		token, err := svcauth.Mint(key, "gates", scope.OrgID, time.Minute)
		return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), err
	}))
	gateconn, gateServer := serve(func(s *grpc.Server) {
		gatesv1.RegisterGatesServiceServer(s, gates.NewGRPCServer(controller, nil, nil, nil))
	})
	gc := gatesv1.NewGatesServiceClient(gateconn)
	moving := &movingTargetGates{GatesServiceClient: gc}
	reviewServer.Merger = &reviews.Merger{Store: store, Git: git, Gates: reviews.GatesClient{Gates: moving}}
	repo, err := git.CreateRepo(ctx, &gitv1.CreateRepoRequest{Name: "pinned"})
	if err != nil {
		t.Fatal(err)
	}
	commit := func(branch string, files map[string]string) string {
		t.Helper()
		var changes []*gitv1.FileChange
		for p, c := range files {
			changes = append(changes, &gitv1.FileChange{Path: p, Content: []byte(c)})
		}
		r, e := git.CreateCommit(ctx, &gitv1.CreateCommitRequest{Repo: repo.Repo.Id, Branch: branch, Message: "evidence", Files: changes})
		if e != nil {
			t.Fatal(e)
		}
		return r.Sha
	}
	spec := "paths:\n  /health:\n    get:\n      responses:\n        '200': {}\n"
	target := commit("main", map[string]string{"api/openapi.yaml": spec, ".novaforge/gates/api.yaml": "name: api-compatibility\nrequired: true\n"})
	if _, err = git.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: repo.Repo.Id, Name: "feature", FromRef: "main"}); err != nil {
		t.Fatal(err)
	}
	source := commit("feature", map[string]string{"README.md": "reviewed\n"})
	run, err := rv.CreateRun(ctx, &reviewsv1.CreateRunRequest{RepoId: repo.Repo.Id, Title: "pinned proof", SourceRef: "feature", TargetRef: "main", AuthorId: uuid.NewString(), AuthorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	runID := uuid.MustParse(run.Run.Id)
	scoped := scopedCtx(org)
	if err = store.SubmitReviewAt(scoped, runID, uuid.New(), "user", "approve", source, "inspected"); err != nil {
		t.Fatal(err)
	}
	if _, err = gc.Evaluate(ctx, &gatesv1.EvaluateRequest{RunId: run.Run.Id}); err != nil {
		t.Fatal(err)
	}
	proof, err := rv.ListProof(ctx, &reviewsv1.ListProofRequest{RunId: run.Run.Id})
	if err != nil || len(proof.GetProof()) != 1 || proof.Proof[0].Producer != "gates" || proof.Proof[0].Status != "pass" {
		t.Fatalf("trusted proof: %v %v", proof, err)
	}
	if _, err = rv.RecordProof(ctx, &reviewsv1.RecordProofRequest{RunId: run.Run.Id, Gate: "api-compatibility", Status: "pass", Detail: "forged"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("wrong producer proof write: %v", err)
	}
	for _, pair := range [][2]string{{source, source}, {target, target}} {
		allowed, _, err := (reviews.GatesClient{Gates: gc}).MayMergePinned(ctx, runID, pair[0], pair[1])
		if allowed {
			t.Fatalf("mismatched pair authorized: %v %v", pair, err)
		}
	}
	foreign, _ := svcauth.Mint(key, "work-reviews", uuid.New(), time.Minute)
	if _, err = gc.Evaluate(metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+foreign)), &gatesv1.EvaluateRequest{RunId: run.Run.Id}); err == nil {
		t.Fatal("foreign organization evaluated run")
	}
	// This is post-authorization/pre-Git rejection at Git's initial check.
	// TestMergeFinalTransactionRejectsMovedTarget covers the later native CAS.
	var movedTarget string
	moving.move = func() { movedTarget = commit("main", map[string]string{"target.txt": "concurrent policy baseline\n"}) }
	moving.advance.Store(true)
	if _, err = rv.MergeRun(ctx, &reviewsv1.MergeRunRequest{RunId: run.Run.Id, Method: "merge"}); err == nil {
		t.Fatal("target moved after authorization but merge succeeded")
	}
	main, err := git.ListCommits(ctx, &gitv1.ListCommitsRequest{Repo: repo.Repo.Id, Ref: "main", Limit: 1})
	if err != nil || len(main.GetCommits()) != 1 || main.Commits[0].Sha != movedTarget {
		t.Fatalf("ref moved despite stale target rejection: %v %v", main, err)
	}
	merged, err := rv.MergeRun(ctx, &reviewsv1.MergeRunRequest{RunId: run.Run.Id, Method: "merge"})
	if err != nil || merged.GetMergeSha() == "" {
		t.Fatalf("merge: %v %v", merged, err)
	}
	// A real deterministic gate failure, not a canned controller denial.
	if _, err = git.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: repo.Repo.Id, Name: "breaking", FromRef: "main"}); err != nil {
		t.Fatal(err)
	}
	breakingHead := commit("breaking", map[string]string{"api/openapi.yaml": "paths: {}\n"})
	breaking, err := rv.CreateRun(ctx, &reviewsv1.CreateRunRequest{RepoId: repo.Repo.Id, Title: "breaking API", SourceRef: "breaking", TargetRef: "main", AuthorId: uuid.NewString(), AuthorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SubmitReviewAt(scoped, uuid.MustParse(breaking.Run.Id), uuid.New(), "user", "approve", breakingHead, "inspected"); err != nil {
		t.Fatal(err)
	}
	if _, err = rv.MergeRun(ctx, &reviewsv1.MergeRunRequest{RunId: breaking.Run.Id, Method: "merge"}); err == nil {
		t.Fatal("breaking API merged")
	}
	failedProof, err := rv.ListProof(ctx, &reviewsv1.ListProofRequest{RunId: breaking.Run.Id})
	if err != nil || len(failedProof.GetProof()) != 1 || failedProof.Proof[0].Status != "fail" || failedProof.Proof[0].Producer != "gates" {
		t.Fatalf("failure proof: %v %v", failedProof, err)
	}
	main, err = git.ListCommits(ctx, &gitv1.ListCommitsRequest{Repo: repo.Repo.Id, Ref: "main", Limit: 1})
	if err != nil || len(main.GetCommits()) != 1 || main.Commits[0].Sha != merged.GetMergeSha() {
		t.Fatalf("failing gate moved target: %v %v", main, err)
	}
	// Actual owner disappearance, not a stub error, must refuse another merge.
	if _, err = git.CreateBranch(ctx, &gitv1.CreateBranchRequest{Repo: repo.Repo.Id, Name: "outage", FromRef: "main"}); err != nil {
		t.Fatal(err)
	}
	outageHead := commit("outage", map[string]string{"outage.txt": "unmerged\n"})
	outage, err := rv.CreateRun(ctx, &reviewsv1.CreateRunRequest{RepoId: repo.Repo.Id, Title: "outage", SourceRef: "outage", TargetRef: "main", AuthorId: uuid.NewString(), AuthorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SubmitReviewAt(scoped, uuid.MustParse(outage.Run.Id), uuid.New(), "user", "approve", outageHead, "inspected"); err != nil {
		t.Fatal(err)
	}
	gateServer.Stop()
	if _, err = rv.MergeRun(ctx, &reviewsv1.MergeRunRequest{RunId: outage.Run.Id, Method: "merge"}); err == nil {
		t.Fatal("merge accepted while gate owner stopped")
	}
	main, err = git.ListCommits(ctx, &gitv1.ListCommitsRequest{Repo: repo.Repo.Id, Ref: "main", Limit: 1})
	if err != nil || len(main.GetCommits()) != 1 || main.Commits[0].Sha != merged.GetMergeSha() {
		t.Fatalf("gate outage moved target: %v %v", main, err)
	}
	if _, err = gc.MayMerge(ctx, &gatesv1.MayMergeRequest{RunId: run.Run.Id, ExpectedSourceSha: source, ExpectedTargetSha: target}); err == nil {
		t.Fatal("stopped gate owner answered")
	}
}
