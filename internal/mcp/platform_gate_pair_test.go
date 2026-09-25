package mcp

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Controlled owner responses exercise the actual authenticated RPC adapter;
// this is a transport contract test, not real datastore/gate acceptance.
type gatePairOwners struct {
	gitv1.UnimplementedGitServiceServer
	reviewsv1.UnimplementedReviewsServiceServer
	gatesv1.UnimplementedGatesServiceServer
	mode string
	mu   sync.Mutex
	pair *gatesv1.MayMergeRequest
}

func (o *gatePairOwners) GetRepo(context.Context, *gitv1.GetRepoRequest) (*gitv1.GetRepoResponse, error) {
	return &gitv1.GetRepoResponse{Repo: &gitv1.Repo{Id: "repo-id"}}, nil
}
func (o *gatePairOwners) GetRun(context.Context, *reviewsv1.GetRunRequest) (*reviewsv1.GetRunResponse, error) {
	run := &reviewsv1.Run{Id: "run-id", RepoId: "repo-id", SourceRef: "feature", TargetRef: "main"}
	if o.mode == "blank-source" {
		run.SourceRef = ""
	}
	return &reviewsv1.GetRunResponse{Run: run}, nil
}
func (o *gatePairOwners) ListCommits(_ context.Context, req *gitv1.ListCommitsRequest) (*gitv1.ListCommitsResponse, error) {
	if req.GetRepo() != "repo-id" || req.GetLimit() != 1 {
		return nil, status.Error(codes.InvalidArgument, "unscoped revision lookup")
	}
	if req.GetRef() == "feature" {
		if o.mode == "source-unavailable" {
			return nil, status.Error(codes.Unavailable, "owner unavailable")
		}
		return &gitv1.ListCommitsResponse{Commits: []*gitv1.Commit{{Sha: "source-sha"}}}, nil
	}
	if req.GetRef() == "main" {
		if o.mode == "target-empty" {
			return &gitv1.ListCommitsResponse{}, nil
		}
		return &gitv1.ListCommitsResponse{Commits: []*gitv1.Commit{{Sha: "target-sha"}}}, nil
	}
	return nil, status.Error(codes.InvalidArgument, "unknown reference")
}
func (o *gatePairOwners) ListEvaluations(context.Context, *gatesv1.ListEvaluationsRequest) (*gatesv1.ListEvaluationsResponse, error) {
	return &gatesv1.ListEvaluationsResponse{}, nil
}
func (o *gatePairOwners) MayMerge(_ context.Context, req *gatesv1.MayMergeRequest) (*gatesv1.MayMergeResponse, error) {
	o.mu.Lock()
	o.pair = req
	o.mu.Unlock()
	out := &gatesv1.MayMergeResponse{Allowed: true, EvaluatedSourceSha: "source-sha", EvaluatedTargetSha: "target-sha"}
	if o.mode == "stale-source" {
		out.EvaluatedSourceSha = "old-source"
	}
	if o.mode == "stale-target" {
		out.EvaluatedTargetSha = "old-target"
	}
	return out, nil
}

func TestPlatformGateStatusPinsAuthenticatedRevisionPair(t *testing.T) {
	for _, mode := range []string{"allowed", "stale-source", "stale-target", "source-unavailable", "target-empty", "blank-source"} {
		t.Run(mode, func(t *testing.T) {
			owners := &gatePairOwners{mode: mode}
			server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
				md, _ := metadata.FromIncomingContext(ctx)
				if len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer fixture-token" || len(md.Get("x-novaforge-org")) != 1 || md.Get("x-novaforge-org")[0] != "team" {
					return nil, status.Error(codes.Unauthenticated, "caller credential/scope was not forwarded")
				}
				return handler(ctx, req)
			}))
			gitv1.RegisterGitServiceServer(server, owners)
			reviewsv1.RegisterReviewsServiceServer(server, owners)
			gatesv1.RegisterGatesServiceServer(server, owners)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			go func() { _ = server.Serve(listener) }()
			defer server.Stop()
			conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			backend := NewPlatformBackend(PlatformClients{Git: gitv1.NewGitServiceClient(conn), Reviews: reviewsv1.NewReviewsServiceClient(conn), Gates: gatesv1.NewGatesServiceClient(conn)})
			out, err := backend.GetGateStatus(context.Background(), Caller{OrgID: "org-id", orgRef: "team", credential: "fixture-token"}, "team", "repo", 1)
			if mode != "allowed" {
				if err == nil {
					t.Fatalf("unqualified pair returned gate status: %s", out)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			owners.mu.Lock()
			pair := owners.pair
			owners.mu.Unlock()
			if pair.GetRunId() != "run-id" || pair.GetExpectedSourceSha() != "source-sha" || pair.GetExpectedTargetSha() != "target-sha" {
				t.Fatalf("gate owner did not receive exact revision pair: %v", pair)
			}
			var result struct {
				Allowed bool `json:"may_merge"`
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil || !result.Allowed {
				t.Fatalf("gate status %s (%v)", out, err)
			}
		})
	}
}
