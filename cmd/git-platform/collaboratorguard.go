package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/authz"
	"github.com/novaforge/novaforge/internal/gitops"
)

// newCollaboratorGuard confines a repository-limited credential to the repositories
// it was granted, over either transport and for reads as well as writes.
//
// A member's access is untouched: this returns immediately for them. For an outside
// collaborator the scope already carries the set they hold, filled when they
// authenticated, so this is a comparison and not another query — and the comparison
// lives in authz.RequireRepo so the rule has one implementation rather than one per
// transport.
//
// A read is confined too. CapFunc is called with no refs for git-upload-pack, which
// is the clone: a guard that only looked at ref updates would let a collaborator
// clone every repository in the organization.
func newCollaboratorGuard(resolve repoIDFunc) gitops.CapFunc {
	return func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error {
		if !s.RepoLimited {
			return nil
		}
		repoID, err := resolve(ctx, orgID, repo)
		if err != nil {
			// The transport's own lookup reports a missing repository; answering
			// here would turn that into a permission error.
			return nil
		}
		scoped := authz.WithScope(ctx, s)
		if err := authz.RequireRepo(scoped, orgID, repoID); err != nil {
			return err
		}
		// A read grant reads. Writing needs the write role, and the ref updates are
		// what makes this a write.
		if len(refs) > 0 {
			if s.RepoRole(repoID) != "write" {
				return fmt.Errorf("this credential was granted read access to %s, not write", repo)
			}
		}
		return nil
	}
}
