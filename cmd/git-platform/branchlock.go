package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/metadata"

	agentsv1 "github.com/novaforge/novaforge/gen/novaforge/agents/v1"
	"github.com/novaforge/novaforge/internal/agents"
	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/gitops"
	"github.com/novaforge/novaforge/internal/svcauth"
)

// lockCheckTimeout bounds one question to agent-runtime. A push waits on it,
// and a slow answer is treated like no answer: refused.
const lockCheckTimeout = 5 * time.Second

// repoIDFunc resolves a repository name in an organization to its id, which
// is how agent-runtime knows the repository a run works in.
type repoIDFunc func(ctx context.Context, orgID uuid.UUID, repo string) (uuid.UUID, error)

func repoIDResolver(pool *pgxpool.Pool) repoIDFunc {
	return func(ctx context.Context, orgID uuid.UUID, repo string) (uuid.UUID, error) {
		var id uuid.UUID
		err := pool.QueryRow(ctx,
			`SELECT id FROM gitplatform.repositories WHERE org_id = $1 AND name = $2`,
			orgID, repo).Scan(&id)
		return id, err
	}
}

// newBranchLockGuard refuses a push to a branch an Agent Run holds, over
// either transport, to everyone but that run's agent (agents.RefuseWrite).
//
// The lock is the run being "running", which lives in agent-runtime's schema;
// git-platform may not read it, so it asks agent-runtime, presenting a service
// token that names only the pushing organization. Only refs in the agent
// namespace are asked about — every agent grant is issued there — so a push
// to main or a feature branch never waits on, or fails because of,
// agent-runtime.
//
// A lock check that cannot be answered refuses the push. Letting it through
// would make a lock that holds only while nothing is wrong.
func newBranchLockGuard(client agentsv1.AgentServiceClient, hmacSecret string, repoID repoIDFunc) gitops.CapFunc {
	return func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		var agentRefs []string
		for _, ref := range refs {
			if strings.HasPrefix(strings.TrimPrefix(ref, "refs/heads/"), agents.BranchNamespace) && strings.HasPrefix(ref, "refs/heads/") {
				agentRefs = append(agentRefs, ref)
			}
		}
		if len(agentRefs) == 0 {
			return nil
		}
		if client == nil {
			return fmt.Errorf("push to %s refused: this git-platform cannot reach agent-runtime to check whether an Agent Run holds the branch", agentRefs[0])
		}

		ctx, cancel := context.WithTimeout(ctx, lockCheckTimeout)
		defer cancel()
		id, err := repoID(ctx, orgID, repo)
		if err != nil {
			return fmt.Errorf("push to %s refused: resolve repository %q: %w", agentRefs[0], repo, err)
		}
		tok, err := svcauth.Mint(hmacSecret, "git-platform", orgID, svcauth.DefaultTTL)
		if err != nil {
			return fmt.Errorf("push to %s refused: mint service token: %w", agentRefs[0], err)
		}
		callCtx := metadata.AppendToOutgoingContext(ctx,
			"authorization", "Bearer "+tok,
			"x-novaforge-org", orgID.String(),
		)
		for _, ref := range agentRefs {
			resp, err := client.CheckBranchLock(callCtx, &agentsv1.CheckBranchLockRequest{RepoId: id.String(), Ref: ref})
			if err != nil {
				return fmt.Errorf("push to %s refused: could not check whether an Agent Run holds it: %w", ref, err)
			}
			if !resp.GetLocked() {
				continue
			}
			runID, _ := uuid.Parse(resp.GetRunId())
			agentID, _ := uuid.Parse(resp.GetAgentId())
			if err := agents.RefuseWrite(s, ref, agents.Holding{RunID: runID, AgentID: agentID, Prefix: resp.GetPrefix()}); err != nil {
				return err
			}
		}
		return nil
	}
}
