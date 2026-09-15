package gates

import (
	"context"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gatesv1 "github.com/novaforge/novaforge/gen/novaforge/gates/v1"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/approvals"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
	"github.com/novaforge/novaforge/internal/secrets"
)

// PutSecret stores a secret's value for an environment. Only an owner or
// admin may: a secret is material every job that names it will receive, so
// who can set it is who decides what runs with it. The answer carries the
// name and environment only — no RPC hands a stored value back to a person.
func (g *GRPCServer) PutSecret(ctx context.Context, req *gatesv1.PutSecretRequest) (*gatesv1.PutSecretResponse, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "no authorization scope for this call")
	}
	if g.Secrets == nil {
		return nil, status.Error(codes.Unimplemented, "this deployment brokers no secrets")
	}
	if !scope.IsOrgAdmin() {
		return nil, status.Error(codes.PermissionDenied, "only an owner or admin of the organization may set a secret")
	}
	if err := secrets.ValidateSecret(req.GetName(), req.GetEnvironment(), req.GetValue()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := g.Secrets.PutValue(ctx, scope.OrgID, req.GetName(), req.GetEnvironment(), req.GetValue()); err != nil {
		return nil, status.Errorf(codes.Internal, "store secret: %v", err)
	}
	return &gatesv1.PutSecretResponse{Secret: &gatesv1.SecretReference{
		Name: req.GetName(), Environment: req.GetEnvironment(),
	}}, nil
}

// maxJobLeaseTTL bounds what a caller may ask for: a lease need outlive only
// the moment a job is dispatched, since the job holds the value once redeemed.
const maxJobLeaseTTL = time.Hour

// defaultJobLeaseTTL is the lease lifetime when the caller names none.
const defaultJobLeaseTTL = 15 * time.Minute

// IssueJobLease issues a lease for one secret to one CI job.
//
// The caller must be a platform service — ci-runner, holding a service token
// for this organization. A person sets secrets and an agent never receives
// one this way. The grant the lease is issued under is derived here, never
// taken from the request:
//
//   - a staging job gets staging values only;
//   - a production job gets production values only when its commit is on the
//     repository's default branch, which a change reaches only by merging —
//     through its gates, an independent review and the approvals its diff
//     raised. On any other branch it is refused, so pushing a branch whose
//     workflow says "environment: production" reads nothing.
func (g *GRPCServer) IssueJobLease(ctx context.Context, req *gatesv1.IssueJobLeaseRequest) (*gatesv1.IssueJobLeaseResponse, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "no authorization scope for this call")
	}
	if scope.ActorKind != "service" {
		return nil, status.Error(codes.PermissionDenied, "job credentials are brokered only to the platform's CI service")
	}
	if g.Secrets == nil {
		return nil, status.Error(codes.Unimplemented, "this deployment brokers no secrets")
	}
	jobID, err := parseUUID("job_id", req.GetJobId())
	if err != nil {
		return nil, err
	}
	repoID, err := parseUUID("repo_id", req.GetRepoId())
	if err != nil {
		return nil, err
	}
	if !secrets.ValidName(req.GetName()) {
		return nil, status.Errorf(codes.InvalidArgument, "invalid secret name %q", req.GetName())
	}
	env := req.GetEnvironment()
	if env == "" {
		env = secrets.EnvironmentStaging
	}

	grant := capability.Grant{OrgID: scope.OrgID, SubjectID: jobID, SubjectKind: "ci"}
	decision, err := approvals.Decide(ctx, approvals.Policy{OrgID: scope.OrgID.String()}, approvals.ActionAccessSecret, grant)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "decide secret access: %v", err)
	}
	if decision != approvals.DecisionPolicy && decision != approvals.DecisionAutomatic {
		// A CI job cannot wait for a person mid-dispatch; anything the policy
		// does not allow outright is refused.
		return nil, status.Errorf(codes.PermissionDenied, "secret access is %s under this organization's policy", decision)
	}

	if env == secrets.EnvironmentProduction {
		if g.DefaultBranch == nil {
			return nil, status.Error(codes.FailedPrecondition, "this deployment cannot resolve a default branch, so it brokers no production credential")
		}
		branch, err := g.DefaultBranch(ctx, repoID)
		if err != nil {
			return nil, statusFromErr(err, codes.Unavailable, "resolve default branch")
		}
		ref := req.GetRef()
		if ref != branch && ref != "refs/heads/"+branch {
			return nil, status.Errorf(codes.PermissionDenied,
				"production credentials are brokered only to a production job on the default branch %q, not %q", branch, ref)
		}
		grant.SecretsProd = true
	}

	ttl := time.Duration(req.GetTtlSeconds()) * time.Second
	if ttl <= 0 || ttl > maxJobLeaseTTL {
		ttl = defaultJobLeaseTTL
	}
	lease, err := g.Secrets.IssueFor(ctx, jobID, grant, req.GetName(), env, ttl)
	if err != nil {
		return nil, status.Errorf(codes.PermissionDenied, "issue lease: %v", err)
	}
	return &gatesv1.IssueJobLeaseResponse{
		LeaseId: lease.ID.String(), Token: lease.Token, Name: lease.Name,
		ExpiresAt: lease.ExpiresAt.Format(rfc3339), Environment: env,
	}, nil
}

// DefaultBranchFromGit resolves a repository's default branch through
// git-platform, with the caller's credential.
func DefaultBranchFromGit(git gitv1.GitServiceClient) func(context.Context, uuid.UUID) (string, error) {
	return func(ctx context.Context, repoID uuid.UUID) (string, error) {
		resp, err := git.GetRepo(ctx, &gitv1.GetRepoRequest{Name: repoID.String()})
		if err != nil {
			return "", err
		}
		if b := resp.GetRepo().GetDefaultBranch(); b != "" {
			return b, nil
		}
		return "", status.Errorf(codes.FailedPrecondition, "repository %s has no default branch", repoID)
	}
}
