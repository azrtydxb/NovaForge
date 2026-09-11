package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"

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

	mu     sync.Mutex
	tokens map[string]cachedToken // key: caller.UserID + "|" + caller.OrgID
}

type cachedToken struct {
	credential string
	org        string
	at         time.Time
}

func newBackend(identity identityv1.IdentityServiceClient, git gitv1.GitServiceClient, work workv1.WorkServiceClient, reviews reviewsv1.ReviewsServiceClient, graph graphv1.GraphServiceClient, gates gatesv1.GatesServiceClient) *backend {
	return &backend{
		identity: identity, git: git, work: work, reviews: reviews, graph: graph, gates: gates,
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

// CreateBranch has no backing RPC yet: git-platform's GitService (see
// proto/novaforge/git/v1/git.proto, which this task does not touch) exposes
// no branch-creation call. Reporting that honestly here is what the
// tools.GitClient adapter in cmd/agent-runtime does for the same gap,
// rather than fabricating a branch that was never created.
func (b *backend) CreateBranch(ctx context.Context, c mcp.Caller, org, repo, branch, from string) (string, error) {
	if err := requireSameOrg(c, org); err != nil {
		return "", err
	}
	return "", fmt.Errorf("git-platform has no branch-creation RPC yet")
}

// GetReview has no backing RPC yet: ReviewsService.GetRun (see
// proto/novaforge/reviews/v1/reviews.proto) looks a run up by its id, and
// nothing exposes a lookup by (repo, run number) — the shape this tool's
// schema calls for.
func (b *backend) GetReview(ctx context.Context, c mcp.Caller, org, repo string, number int) (string, error) {
	if err := requireSameOrg(c, org); err != nil {
		return "", err
	}
	return "", fmt.Errorf("reviews service has no lookup-by-number RPC yet (run #%d)", number)
}

// RunCI has no backing RPC yet: ci-runner's only gRPC surface is
// RunnerService (register/connect/report-status, see
// proto/novaforge/ci/v1/ci.proto); CI runs are currently scheduled only in
// response to a push event, not dispatched on demand.
func (b *backend) RunCI(ctx context.Context, c mcp.Caller, org, repo, ref string) (string, error) {
	if err := requireSameOrg(c, org); err != nil {
		return "", err
	}
	return "", fmt.Errorf("ci-runner has no run-dispatch RPC yet; CI runs are scheduled automatically on push")
}

// GetGateStatus has no backing RPC that resolves by (repo, run number):
// GatesService.ListEvaluations (see proto/novaforge/gates/v1/gates.proto)
// takes a run id.
func (b *backend) GetGateStatus(ctx context.Context, c mcp.Caller, org, repo string, number int) (string, error) {
	if err := requireSameOrg(c, org); err != nil {
		return "", err
	}
	return "", fmt.Errorf("gates service has no lookup-by-number RPC yet (run #%d)", number)
}
