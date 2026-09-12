package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
	"github.com/novaforge/novaforge/internal/mcp"
)

// tokenTTL bounds how long backend caches the raw credential behind a
// resolved mcp.Caller. mcp.Server calls Backend.Authenticate and the
// specific tool method back to back within the same tools/call request
// (see internal/mcp/server.go's callTool), so this only needs to survive a
// single request's round trip — the short TTL exists purely to bound the
// cache's size against an abandoned entry, not to keep sessions alive.
const tokenTTL = 30 * time.Second

// backend implements mcp.Backend over the platform's gRPC services.
//
// A credential says who the caller is, not which organization they act in
// (see cmd/git-platform/main.go's own doc on this). The MCP protocol gives
// a session exactly one bearer token with no secondary per-call channel for
// an organization header, so the token this backend accepts is expected in
// the compound form "<org>:<credential>" — minted by whatever issues MCP
// credentials for a given organization. Authenticate resolves both halves
// through the identity service exactly like every other service's auth
// interceptor, and every subsequent tool call's own "org" argument is
// checked against the resolved Caller.OrgID before anything is done in its
// name, so a caller cannot simply name a different org in a tool call and
// be believed.
type backend struct {
	identity identityv1.IdentityServiceClient
	git      gitv1.GitServiceClient
	work     workv1.WorkServiceClient
	reviews  reviewsv1.ReviewsServiceClient
	graph    graphv1.GraphServiceClient
	gates    gatesv1.GatesServiceClient
	ci       civ1.CIServiceClient

	mu     sync.Mutex
	tokens map[string]cachedToken // key: caller.UserID + "|" + caller.OrgID
}

type cachedToken struct {
	credential string
	org        string
	at         time.Time
}

func newBackend(identity identityv1.IdentityServiceClient, git gitv1.GitServiceClient, work workv1.WorkServiceClient, reviews reviewsv1.ReviewsServiceClient, graph graphv1.GraphServiceClient, gates gatesv1.GatesServiceClient, ci civ1.CIServiceClient) *backend {
	return &backend{
		identity: identity, git: git, work: work, reviews: reviews, graph: graph, gates: gates, ci: ci,
		tokens: make(map[string]cachedToken),
	}
}

func callerKey(userID, orgID string) string { return userID + "|" + orgID }

// Authenticate resolves token (formatted "<org>:<credential>") through the
// identity service and caches the credential half, keyed by the resulting
// Caller, so the tool method invoked immediately afterward in the same
// request can authenticate its own outbound gRPC calls.
func (b *backend) Authenticate(ctx context.Context, token string) (mcp.Caller, error) {
	org, credential, ok := strings.Cut(token, ":")
	if !ok || org == "" || credential == "" {
		return mcp.Caller{}, fmt.Errorf(`malformed MCP token: want "<org>:<credential>"`)
	}

	subject, err := b.resolveSubject(ctx, credential, org)
	if err != nil {
		return mcp.Caller{}, fmt.Errorf("resolve credential: %w", err)
	}
	caller := mcp.Caller{UserID: subject.GetUserId(), OrgID: org}

	b.mu.Lock()
	b.evictExpiredLocked()
	b.tokens[callerKey(caller.UserID, caller.OrgID)] = cachedToken{credential: credential, org: org, at: time.Now()}
	b.mu.Unlock()

	return caller, nil
}

func (b *backend) resolveSubject(ctx context.Context, credential, org string) (*identityv1.Subject, error) {
	if resp, err := b.identity.ResolveToken(ctx, &identityv1.ResolveTokenRequest{Token: credential, Org: org}); err == nil {
		return resp.GetSubject(), nil
	}
	resp, err := b.identity.ResolveSession(ctx, &identityv1.ResolveSessionRequest{Token: credential, Org: org})
	if err != nil {
		return nil, err
	}
	return resp.GetSubject(), nil
}

func (b *backend) evictExpiredLocked() {
	now := time.Now()
	for k, v := range b.tokens {
		if now.Sub(v.at) > tokenTTL {
			delete(b.tokens, k)
		}
	}
}

// authedContext attaches c's cached credential and organization as outbound
// gRPC metadata, so the target service's own auth interceptor resolves the
// identical authz.Scope every other client of that service gets.
func (b *backend) authedContext(ctx context.Context, c mcp.Caller) (context.Context, error) {
	b.mu.Lock()
	tok, ok := b.tokens[callerKey(c.UserID, c.OrgID)]
	b.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("session expired; reauthenticate")
	}
	md := metadata.Pairs("authorization", "Bearer "+tok.credential, "x-novaforge-org", tok.org)
	return metadata.NewOutgoingContext(ctx, md), nil
}

func requireSameOrg(c mcp.Caller, org string) error {
	if org != c.OrgID {
		return fmt.Errorf("denied: cross-org access")
	}
	return nil
}

// resolveRepoID looks up a repository's id from its name, within the
// caller's organization — the git transport RPCs address a repo by name,
// while graph and reviews address it by id.
func (b *backend) resolveRepoID(ctx context.Context, repo string) (string, error) {
	resp, err := b.git.ListRepos(ctx, &gitv1.ListReposRequest{})
	if err != nil {
		return "", fmt.Errorf("list repos: %w", err)
	}
	for _, r := range resp.GetRepos() {
		if r.GetName() == repo {
			return r.GetId(), nil
		}
	}
	return "", fmt.Errorf("repository %q not found", repo)
}

var marshalOpts = protojson.MarshalOptions{EmitUnpopulated: true}

func (b *backend) GetWorkItem(ctx context.Context, c mcp.Caller, org, repo, key string) (string, error) {
	if err := requireSameOrg(c, org); err != nil {
		return "", err
	}
	authed, err := b.authedContext(ctx, c)
	if err != nil {
		return "", err
	}
	resp, err := b.work.GetItem(authed, &workv1.GetItemRequest{Key: key})
	if err != nil {
		return "", fmt.Errorf("get work item %q: %w", key, err)
	}
	return marshalOpts.Format(resp.GetItem()), nil
}

func (b *backend) SearchRepository(ctx context.Context, c mcp.Caller, org, repo, query string) (string, error) {
	if err := requireSameOrg(c, org); err != nil {
		return "", err
	}
	authed, err := b.authedContext(ctx, c)
	if err != nil {
		return "", err
	}
	repoID, err := b.resolveRepoID(authed, repo)
	if err != nil {
		return "", err
	}
	resp, err := b.graph.SearchCode(authed, &graphv1.SearchCodeRequest{RepoId: repoID, Query: query, K: 20})
	if err != nil {
		return "", fmt.Errorf("search repository %q: %w", repo, err)
	}
	return marshalOpts.Format(resp), nil
}

func (b *backend) GetSymbol(ctx context.Context, c mcp.Caller, org, repo, name string) (string, error) {
	if err := requireSameOrg(c, org); err != nil {
		return "", err
	}
	authed, err := b.authedContext(ctx, c)
	if err != nil {
		return "", err
	}
	repoID, err := b.resolveRepoID(authed, repo)
	if err != nil {
		return "", err
	}
	resp, err := b.graph.GetSymbol(authed, &graphv1.GetSymbolRequest{RepoId: repoID, Name: name})
	if err != nil {
		return "", fmt.Errorf("get symbol %q: %w", name, err)
	}
	return marshalOpts.Format(resp.GetSymbol()), nil
}

// CreateBranch points a new branch at from, or at the repository's default
// branch when from is empty.
func (b *backend) CreateBranch(ctx context.Context, c mcp.Caller, org, repo, branch, from string) (string, error) {
	if err := requireSameOrg(c, org); err != nil {
		return "", err
	}
	authed, err := b.authedContext(ctx, c)
	if err != nil {
		return "", err
	}
	resp, err := b.git.CreateBranch(authed, &gitv1.CreateBranchRequest{Repo: repo, Name: branch, FromRef: from})
	if err != nil {
		return "", fmt.Errorf("create branch %q: %w", branch, err)
	}
	return marshalOpts.Format(resp.GetRef()), nil
}

// GetReview looks a review run up as people address it — "run #7 on this
// repository".
func (b *backend) GetReview(ctx context.Context, c mcp.Caller, org, repo string, number int) (string, error) {
	if err := requireSameOrg(c, org); err != nil {
		return "", err
	}
	authed, err := b.authedContext(ctx, c)
	if err != nil {
		return "", err
	}
	run, err := b.reviewRunByNumber(authed, repo, number)
	if err != nil {
		return "", err
	}
	return marshalOpts.Format(run), nil
}

// reviewRunByNumber resolves (repo, number) to a run, which both GetReview
// and GetGateStatus need — a gate evaluation is addressed by run id, but a
// person names the run by its number.
func (b *backend) reviewRunByNumber(authed context.Context, repo string, number int) (*reviewsv1.Run, error) {
	repoID, err := b.resolveRepoID(authed, repo)
	if err != nil {
		return nil, err
	}
	resp, err := b.reviews.GetRun(authed, &reviewsv1.GetRunRequest{RepoId: repoID, Number: int32(number)})
	if err != nil {
		return nil, fmt.Errorf("get review run #%d: %w", number, err)
	}
	return resp.GetRun(), nil
}

// RunCI schedules a run of the repository's workflow at ref, on demand.
func (b *backend) RunCI(ctx context.Context, c mcp.Caller, org, repo, ref string) (string, error) {
	if err := requireSameOrg(c, org); err != nil {
		return "", err
	}
	authed, err := b.authedContext(ctx, c)
	if err != nil {
		return "", err
	}
	repoID, err := b.resolveRepoID(authed, repo)
	if err != nil {
		return "", err
	}
	resp, err := b.ci.TriggerRun(authed, &civ1.TriggerRunRequest{RepoId: repoID, Ref: ref})
	if err != nil {
		return "", fmt.Errorf("trigger a CI run on %s: %w", repo, err)
	}
	return marshalOpts.Format(resp.GetRun()), nil
}

// GetGateStatus reports the gate evaluations of a review run, addressed by
// its number. The run is resolved first because gates are recorded against
// a run id.
func (b *backend) GetGateStatus(ctx context.Context, c mcp.Caller, org, repo string, number int) (string, error) {
	if err := requireSameOrg(c, org); err != nil {
		return "", err
	}
	authed, err := b.authedContext(ctx, c)
	if err != nil {
		return "", err
	}
	run, err := b.reviewRunByNumber(authed, repo, number)
	if err != nil {
		return "", err
	}
	resp, err := b.gates.ListEvaluations(authed, &gatesv1.ListEvaluationsRequest{RunId: run.GetId()})
	if err != nil {
		return "", fmt.Errorf("list gate evaluations for run #%d: %w", number, err)
	}
	return marshalOpts.Format(resp), nil
}
