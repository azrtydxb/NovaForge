package gitops

import (
	"context"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// forkRepo copies srcRepoID into a new repository named name, owned by toOrg,
// and records which repository it came from. The RPC of the same name in
// grpc_fork.go is the only caller; the work is here so the gRPC method is left
// doing nothing but parsing its request.
//
// A fork is a full repository, not a reference to its parent: the objects are
// copied, the origin remote is removed, and nothing on any read or write path
// consults the parent. Sharing the parent's object store through
// objects/info/alternates would have been one command shorter and would empty
// every fork the day the parent is deleted or its objects are pruned, and a
// fork whose refs live in the parent's namespace is a repository whose pushes
// land in someone else's history — the exact failure this task has to rule out.
//
// toOrg must be the caller's own organization. Organizations are a hard
// security boundary here: a fork into another organization would place one
// organization's entire history inside another, which no credential on either
// side authorizes, so it is refused rather than quietly permitted. Contributing
// across organizations is done by forking inside your own and being granted
// access to the upstream, not by the platform moving history for you.
func (s *Server) forkRepo(ctx context.Context, srcRepoID, toOrg uuid.UUID, name string) (repoRow, error) {
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return repoRow{}, err
	}
	if toOrg != uuid.Nil && toOrg != scope.OrgID {
		return repoRow{}, status.Error(codes.PermissionDenied,
			"a repository can only be forked into your own organization")
	}
	parent, err := s.repoByName(ctx, scope.OrgID, srcRepoID.String())
	if err != nil {
		return repoRow{}, err
	}
	if name == "" {
		// Forking a repository into the same organization under the same name is
		// what a caller who named nothing asked for, and the unique constraint
		// refuses it. Saying so beats "repository already exists" on a request
		// that never mentioned a name.
		return repoRow{}, status.Error(codes.InvalidArgument, "the fork needs a name of its own")
	}
	if !repoNameRe.MatchString(name) {
		return repoRow{}, status.Errorf(codes.InvalidArgument, "invalid repository name %q", name)
	}

	parentRepo, err := Open(s.root, parent.OrgID, parent.Name)
	if err != nil {
		return repoRow{}, status.Errorf(codes.Internal, "resolve parent repository path: %v", err)
	}
	if _, err := os.Stat(parentRepo.Path()); err != nil {
		return repoRow{}, status.Errorf(codes.FailedPrecondition, "the repository has no history on disk to fork")
	}

	fork := repoRow{
		ID:            uuid.New(),
		OrgID:         scope.OrgID,
		Name:          name,
		DefaultBranch: parent.DefaultBranch,
		ParentRepoID:  parent.ID,
	}
	const insert = `
		INSERT INTO gitplatform.repositories (id, org_id, name, default_branch, parent_repo_id)
		VALUES ($1, $2, $3, $4, $5)`
	if _, err := s.pool.Exec(ctx, insert, fork.ID, fork.OrgID, fork.Name, fork.DefaultBranch, fork.ParentRepoID); err != nil {
		var pgErr *pgconn.PgError
		if isUniqueViolation(err, &pgErr) {
			return repoRow{}, status.Errorf(codes.AlreadyExists, "repository %q already exists", name)
		}
		return repoRow{}, status.Errorf(codes.Internal, "create fork record: %v", err)
	}
	if err := cloneForFork(parentRepo.Path(), s.root, fork.OrgID, fork.Name); err != nil {
		// The same best-effort cleanup CreateRepo does: a metadata row with no
		// repository behind it can never be created again and answers every
		// read with a git error.
		_, _ = s.pool.Exec(ctx, `DELETE FROM gitplatform.repositories WHERE id = $1`, fork.ID)
		return repoRow{}, status.Errorf(codes.Internal, "copy repository history: %v", err)
	}
	return fork, nil
}

// cloneForFork copies srcPath into the fork's own bare repository.
//
// --no-hardlinks is deliberate: a hardlinked clone is cheap and safe as long as
// nothing ever rewrites an object file, which is an invariant of git rather than
// of this platform's storage, backups or per-repository quotas. A fork is meant
// to be independent of the parent on disk too.
//
// The origin remote is then removed. A bare clone leaves one behind pointing at
// the parent, and a plain `git fetch` in a repository configured that way can
// move the fork's own refs to whatever the parent has — a fork must never have
// its history overwritten by its upstream as a side effect of maintenance.
func cloneForFork(srcPath, root string, orgID uuid.UUID, name string) error {
	forkPath, err := resolvePath(root, orgID, name)
	if err != nil {
		return err
	}
	if _, err := run("", "clone", "--bare", "--no-hardlinks", srcPath, forkPath); err != nil {
		return err
	}
	if _, err := run(forkPath, "remote", "remove", "origin"); err != nil {
		// A clone of an empty repository has no origin to remove on some git
		// builds; nothing else can fail here, and leaving the remote configured
		// is the one outcome that matters, so the repository is checked instead
		// of the command's exit status.
		if out, cfgErr := run(forkPath, "config", "--get", "remote.origin.url"); cfgErr == nil {
			return fmt.Errorf("fork still points at its parent: %s", out)
		}
	}
	return nil
}
