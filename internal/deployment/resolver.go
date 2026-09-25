package deployment

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/capability"
)

// NewRunResolver consults the owner on every authorization. Connections must
// forward the original caller credential; persisted run fields confer no scope.
// Actor kind selects the namespace, so a missing agent run never falls back to
// a coincidentally matching human Engineering Run UUID.
func NewRunResolver(agents agentsv1.AgentServiceClient, identity identityv1.IdentityServiceClient, reviews reviewsv1.ReviewsServiceClient) RunResolver {
	return func(ctx context.Context, id uuid.UUID) (Run, error) {
		deny := errors.New("deployment run is unavailable or outside caller authority")
		scope, err := authz.FromContext(ctx)
		if err != nil || id == uuid.Nil || scope.OrgID == uuid.Nil || scope.ActorID == uuid.Nil {
			return Run{}, deny
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		run := Run{ID: id, OrgID: scope.OrgID, ActorID: scope.ActorID, ActorKind: scope.ActorKind}
		switch scope.ActorKind {
		case "agent":
			if agents == nil || identity == nil {
				return Run{}, deny
			}
			response, err := agents.GetRun(ctx, &agentsv1.GetRunRequest{Id: id.String()})
			if err != nil {
				return Run{}, deny
			}
			r := response.GetRun()
			if r.GetId() != id.String() || r.GetOrgId() != scope.OrgID.String() || r.GetAgentId() != scope.ActorID.String() || r.GetState() != "running" {
				return Run{}, deny
			}
			run.RepoID, err = uuid.Parse(r.GetRepoId())
			if err != nil || run.RepoID == uuid.Nil {
				return Run{}, deny
			}
			started, err := time.Parse(time.RFC3339Nano, r.GetStartedAt())
			seconds := r.GetWallclockLimitSeconds()
			if err != nil || seconds <= 0 || seconds > int64((1<<63-1)/time.Second) {
				return Run{}, deny
			}
			run.ExpiresAt = started.Add(time.Duration(seconds) * time.Second)
			if !run.ExpiresAt.After(time.Now()) {
				return Run{}, deny
			}
			grantID, err := uuid.Parse(r.GetGrantId())
			if err != nil || grantID == uuid.Nil {
				return Run{}, deny
			}
			responseGrant, err := identity.GetGrant(ctx, &identityv1.GetGrantRequest{Id: grantID.String()})
			if err != nil {
				return Run{}, deny
			}
			g := responseGrant.GetGrant()
			if g.GetId() != grantID.String() || g.GetOrgId() != scope.OrgID.String() || g.GetSubjectId() != scope.ActorID.String() || g.GetSubjectKind() != "agent" {
				return Run{}, deny
			}
			expiry, err := time.Parse(time.RFC3339Nano, g.GetExpiresAt())
			if err != nil || !expiry.After(time.Now()) {
				return Run{}, deny
			}
			run.Grant = capability.Grant{ID: grantID, OrgID: scope.OrgID, SubjectID: scope.ActorID, SubjectKind: "agent", RepoRead: g.GetRepoRead(), WriteBranch: g.GetWriteBranch(), DeployStaging: g.GetDeployStaging(), DeployProd: g.GetDeployProd(), SecretsProd: g.GetSecretsProd(), ExpiresAt: expiry}
		case "user":
			if reviews == nil || (scope.Role != "member" && !scope.IsOrgAdmin()) {
				return Run{}, deny
			}
			response, err := reviews.GetRun(ctx, &reviewsv1.GetRunRequest{Id: id.String()})
			if err != nil {
				return Run{}, deny
			}
			r := response.GetRun()
			if r.GetId() != id.String() || r.GetOrgId() != scope.OrgID.String() || r.GetAuthorId() != scope.ActorID.String() || r.GetAuthorKind() != "user" || (r.GetState() != "open" && r.GetState() != "merged") {
				return Run{}, deny
			}
			run.RepoID, err = uuid.Parse(r.GetRepoId())
			if err != nil || run.RepoID == uuid.Nil {
				return Run{}, deny
			}
		default:
			return Run{}, deny
		}
		return run, nil
	}
}
