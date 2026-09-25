package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"

	civ1 "github.com/novaforge/novaforge/gen/novaforge/ci/v1"
	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	graphv1 "github.com/novaforge/novaforge/gen/novaforge/graph/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	workv1 "github.com/novaforge/novaforge/gen/novaforge/work/v1"
)

// PlatformClients are the services the MCP tools translate onto. Each is a
// plain client with no credential interceptor: PlatformBackend attaches the
// MCP caller's own credential to every call it makes.
type PlatformClients struct {
	Identity identityv1.IdentityServiceClient
	Git      gitv1.GitServiceClient
	Work     workv1.WorkServiceClient
	Reviews  reviewsv1.ReviewsServiceClient
	Graph    graphv1.GraphServiceClient
	Gates    gatesv1.GatesServiceClient
	CI       civ1.CIServiceClient
}

// PlatformBackend implements Backend over the platform's gRPC services. It is
// the backend mcp-server runs; it lived in cmd/mcp-server, where no test ever
// reached it, so not one of the seven tools had been seen to succeed.
//
// A credential says who the caller is, not which organization they act in.
// An MCP session carries exactly one bearer token and no other per-call
// channel, so the token is the compound "<org>:<credential>". Authenticate
// resolves both halves through identity, exactly like every service's auth
// interceptor, and every tool call's own "org" argument must name that same
// organization before anything is done in its name.
type PlatformBackend struct {
	c PlatformClients
}

// NewPlatformBackend returns the production backend over c.
func NewPlatformBackend(c PlatformClients) *PlatformBackend {
	return &PlatformBackend{c: c}
}

// Authenticate resolves token (formatted "<org>:<credential>") through the
// identity service. The credential travels on the returned Caller to the tool
// call made in the same request — it used to be parked in a cache keyed by
// user and organization, which let two sessions of one person borrow each
// other's credential and kept a credential alive past its request.
func (b *PlatformBackend) Authenticate(ctx context.Context, token string) (Caller, error) {
	org, credential, ok := strings.Cut(token, ":")
	if !ok || org == "" || credential == "" {
		return Caller{}, fmt.Errorf(`malformed MCP token: want "<org>:<credential>"`)
	}
	subject, err := b.resolveSubject(ctx, credential, org)
	if err != nil {
		return Caller{}, fmt.Errorf("resolve credential: %w", err)
	}
	if subject.GetOrgId() == "" {
		return Caller{}, fmt.Errorf("the credential is not scoped to organization %q", org)
	}
	return Caller{
		UserID:     subject.GetUserId(),
		OrgID:      subject.GetOrgId(),
		orgRef:     org,
		credential: credential,
	}, nil
}

func (b *PlatformBackend) resolveSubject(ctx context.Context, credential, org string) (*identityv1.Subject, error) {
	if resp, err := b.c.Identity.ResolveToken(ctx, &identityv1.ResolveTokenRequest{Token: credential, Org: org}); err == nil {
		return resp.GetSubject(), nil
	}
	resp, err := b.c.Identity.ResolveSession(ctx, &identityv1.ResolveSessionRequest{Token: credential, Org: org})
	if err != nil {
		return nil, err
	}
	return resp.GetSubject(), nil
}

// authed checks the tool's org argument against the caller and returns a
// context carrying the caller's credential and organization as outbound
// metadata, so the target service resolves the identical scope it would for
// any other client.
func (b *PlatformBackend) authed(ctx context.Context, c Caller, org string) (context.Context, error) {
	if org != c.OrgID && org != c.orgRef {
		return nil, fmt.Errorf("denied: cross-org access")
	}
	if c.credential == "" {
		return nil, fmt.Errorf("unauthorized: no credential on this call")
	}
	md := metadata.Pairs("authorization", "Bearer "+c.credential, "x-novaforge-org", c.orgRef)
	return metadata.NewOutgoingContext(ctx, md), nil
}

// resolveRepoID looks up a repository's id from its name, within the
// caller's organization — the git RPCs address a repo by name, while graph
// and reviews address it by id.
func (b *PlatformBackend) resolveRepoID(ctx context.Context, repo string) (string, error) {
	resp, err := b.c.Git.GetRepo(ctx, &gitv1.GetRepoRequest{Name: repo})
	if err != nil {
		return "", fmt.Errorf("repository %q: %w", repo, err)
	}
	return resp.GetRepo().GetId(), nil
}

var marshalOpts = protojson.MarshalOptions{EmitUnpopulated: true}

// GetWorkItem fetches a Work Item by key, and checks it belongs to the named
// repository: a key is unique in an organization, and a caller asking for one
// repository's item must not be answered with another's.
func (b *PlatformBackend) GetWorkItem(ctx context.Context, c Caller, org, repo, key string) (string, error) {
	authed, err := b.authed(ctx, c, org)
	if err != nil {
		return "", err
	}
	resp, err := b.c.Work.GetItem(authed, &workv1.GetItemRequest{Key: key})
	if err != nil {
		return "", fmt.Errorf("get work item %q: %w", key, err)
	}
	if repo != "" {
		repoID, err := b.resolveRepoID(authed, repo)
		if err != nil {
			return "", err
		}
		if resp.GetItem().GetRepoId() != repoID {
			return "", fmt.Errorf("work item %q is not in repository %q", key, repo)
		}
	}
	return marshalOpts.Format(resp.GetItem()), nil
}

// SearchRepository searches a repository's indexed code.
func (b *PlatformBackend) SearchRepository(ctx context.Context, c Caller, org, repo, query string) (string, error) {
	authed, err := b.authed(ctx, c, org)
	if err != nil {
		return "", err
	}
	if b.c.Graph == nil {
		return "", fmt.Errorf("search needs the engineering-graph service, which this deployment has not configured")
	}
	repoID, err := b.resolveRepoID(authed, repo)
	if err != nil {
		return "", err
	}
	resp, err := b.c.Graph.SearchCode(authed, &graphv1.SearchCodeRequest{RepoId: repoID, Query: query, K: 20})
	if err != nil {
		return "", fmt.Errorf("search repository %q: %w", repo, err)
	}
	return marshalOpts.Format(resp), nil
}

// GetSymbol resolves a symbol through the graph.
func (b *PlatformBackend) GetSymbol(ctx context.Context, c Caller, org, repo, name string) (string, error) {
	authed, err := b.authed(ctx, c, org)
	if err != nil {
		return "", err
	}
	if b.c.Graph == nil {
		return "", fmt.Errorf("symbol lookup needs the engineering-graph service, which this deployment has not configured")
	}
	repoID, err := b.resolveRepoID(authed, repo)
	if err != nil {
		return "", err
	}
	resp, err := b.c.Graph.GetSymbol(authed, &graphv1.GetSymbolRequest{RepoId: repoID, Name: name})
	if err != nil {
		return "", fmt.Errorf("get symbol %q: %w", name, err)
	}
	return marshalOpts.Format(resp.GetSymbol()), nil
}

// CreateBranch points a new branch at from, or at the repository's default
// branch when from is empty.
func (b *PlatformBackend) CreateBranch(ctx context.Context, c Caller, org, repo, branch, from string) (string, error) {
	authed, err := b.authed(ctx, c, org)
	if err != nil {
		return "", err
	}
	resp, err := b.c.Git.CreateBranch(authed, &gitv1.CreateBranchRequest{Repo: repo, Name: branch, FromRef: from})
	if err != nil {
		return "", fmt.Errorf("create branch %q: %w", branch, err)
	}
	return marshalOpts.Format(resp.GetRef()), nil
}

// GetReview looks a review run up as people address it — "run #7 on this
// repository".
func (b *PlatformBackend) GetReview(ctx context.Context, c Caller, org, repo string, number int) (string, error) {
	authed, err := b.authed(ctx, c, org)
	if err != nil {
		return "", err
	}
	run, err := b.reviewRunByNumber(authed, repo, number)
	if err != nil {
		return "", err
	}
	return marshalOpts.Format(run), nil
}

func (b *PlatformBackend) reviewRunByNumber(authed context.Context, repo string, number int) (*reviewsv1.Run, error) {
	repoID, err := b.resolveRepoID(authed, repo)
	if err != nil {
		return nil, err
	}
	resp, err := b.c.Reviews.GetRun(authed, &reviewsv1.GetRunRequest{RepoId: repoID, Number: int32(number)})
	if err != nil {
		return nil, fmt.Errorf("get review run #%d: %w", number, err)
	}
	return resp.GetRun(), nil
}

// RunCI schedules a run of the repository's workflow at ref, on demand.
func (b *PlatformBackend) RunCI(ctx context.Context, c Caller, org, repo, ref string) (string, error) {
	authed, err := b.authed(ctx, c, org)
	if err != nil {
		return "", err
	}
	if b.c.CI == nil {
		return "", fmt.Errorf("running CI needs the ci-runner service, which this deployment has not configured")
	}
	repoID, err := b.resolveRepoID(authed, repo)
	if err != nil {
		return "", err
	}
	resp, err := b.c.CI.TriggerRun(authed, &civ1.TriggerRunRequest{RepoId: repoID, Ref: ref})
	if err != nil {
		return "", fmt.Errorf("trigger a CI run on %s: %w", repo, err)
	}
	return marshalOpts.Format(resp.GetRun()), nil
}

// gateRefHead resolves through the Git owner with the same caller credential.
// An absent ref must not fall back to HEAD and invent a policy baseline.
func (b *PlatformBackend) gateRefHead(ctx context.Context, repoID, ref string) (string, error) {
	if repoID == "" || ref == "" {
		return "", fmt.Errorf("gate status needs an explicit repository and reference")
	}
	resp, err := b.c.Git.ListCommits(ctx, &gitv1.ListCommitsRequest{Repo: repoID, Ref: ref, Limit: 1})
	if err != nil {
		return "", fmt.Errorf("resolve gate reference %q: %w", ref, err)
	}
	if len(resp.GetCommits()) != 1 || resp.GetCommits()[0].GetSha() == "" {
		return "", fmt.Errorf("gate reference %q has no unambiguous commit", ref)
	}
	return resp.GetCommits()[0].GetSha(), nil
}

// GetGateStatus reports the gate evaluations of a review run, addressed by
// its number, together with whether the run may merge right now. The
// evaluations alone cannot say that: a gate that was never evaluated has no
// row, and an absent row reads as nothing wrong.
func (b *PlatformBackend) GetGateStatus(ctx context.Context, c Caller, org, repo string, number int) (string, error) {
	authed, err := b.authed(ctx, c, org)
	if err != nil {
		return "", err
	}
	if b.c.Gates == nil || b.c.Git == nil || b.c.Reviews == nil {
		return "", fmt.Errorf("gate status needs the gates, Git and reviews services")
	}
	run, err := b.reviewRunByNumber(authed, repo, number)
	if err != nil {
		return "", err
	}
	source, err := b.gateRefHead(authed, run.GetRepoId(), run.GetSourceRef())
	if err != nil {
		return "", err
	}
	target, err := b.gateRefHead(authed, run.GetRepoId(), run.GetTargetRef())
	if err != nil {
		return "", err
	}
	evals, err := b.c.Gates.ListEvaluations(authed, &gatesv1.ListEvaluationsRequest{RunId: run.GetId()})
	if err != nil {
		return "", fmt.Errorf("list gate evaluations for run #%d: %w", number, err)
	}
	may, err := b.c.Gates.MayMerge(authed, &gatesv1.MayMergeRequest{RunId: run.GetId(), ExpectedSourceSha: source, ExpectedTargetSha: target})
	if err != nil {
		return "", fmt.Errorf("ask whether run #%d may merge: %w", number, err)
	}
	// Even an allowed response from an older or stale owner cannot authorize
	// a different source/policy baseline than the caller actually resolved.
	if may.GetEvaluatedSourceSha() != source || may.GetEvaluatedTargetSha() != target {
		return "", fmt.Errorf("gate owner did not evaluate the requested source/target pair")
	}
	reasons := may.GetReasons()
	if reasons == nil {
		reasons = []string{}
	}
	out, err := json.Marshal(struct {
		Run         int             `json:"run"`
		MayMerge    bool            `json:"may_merge"`
		SourceSHA   string          `json:"source_sha"`
		TargetSHA   string          `json:"target_sha"`
		Reasons     []string        `json:"reasons"`
		Evaluations json.RawMessage `json:"evaluations"`
	}{number, may.GetAllowed(), source, target, reasons, json.RawMessage(marshalOpts.Format(evals))})
	if err != nil {
		return "", fmt.Errorf("encode gate status: %w", err)
	}
	return string(out), nil
}
