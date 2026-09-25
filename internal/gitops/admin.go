package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrArchived is returned for a write to an archived repository. The refusal names
// the archive: "permission denied" would send someone looking for a missing grant
// that is not the reason.
var ErrArchived = errors.New("repository is archived and accepts no writes")

// A repository's directory is root/<org>/<name>.git, so its name and its owning
// organization are both part of its path: a rename and a transfer move files as
// well as rows. Each moves the directory and then commits, and moves it back if
// the commit fails, so the row and the filesystem cannot end up disagreeing. The
// alternative — deriving the path from the immutable id — is better and would mean
// relaying every existing repository's storage, which is not this change.

// UpdateRepo renames a repository, changes its default branch, or archives it.
//
// Each field is applied only when the caller asked for it. An empty name meaning
// "rename to empty" or a false archived meaning "un-archive" would make any partial
// update silently reset what it did not mention.
func (s *Server) UpdateRepo(ctx context.Context, req *gitv1.UpdateRepoRequest) (*gitv1.UpdateRepoResponse, error) {
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := s.repoByName(ctx, scope.OrgID, req.GetRepo())
	if err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin update: %v", err)
	}
	defer tx.Rollback(ctx)

	name := repo.Name
	if req.GetName() != "" && req.GetName() != repo.Name {
		if !repoNameRe.MatchString(req.GetName()) {
			return nil, status.Errorf(codes.InvalidArgument, "invalid repository name %q", req.GetName())
		}
		name = req.GetName()
	}
	branch := repo.DefaultBranch
	if req.GetDefaultBranch() != "" {
		branch = req.GetDefaultBranch()
	}
	archived := repo.Archived
	if req.GetSetArchived() {
		archived = req.GetArchived()
	}

	if _, err := tx.Exec(ctx,
		`UPDATE gitplatform.repositories SET name = $1, default_branch = $2, archived = $3
		 WHERE id = $4 AND org_id = $5`,
		name, branch, archived, repo.ID, scope.OrgID); err != nil {
		return nil, status.Errorf(codes.Internal, "update repository: %v", err)
	}

	// The directory move happens before the commit so a failed move leaves the
	// transaction to roll back with nothing changed.
	var rollback func()
	if name != repo.Name {
		undo, err := moveRepoDir(s.root, scope.OrgID, scope.OrgID, repo.Name, name)
		if err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "rename repository: %v", err)
		}
		rollback = undo
	}
	// A default branch has to exist, or a fresh clone checks out nothing. This is
	// the repository's own HEAD, not a database opinion about it.
	if branch != repo.DefaultBranch {
		path, err := resolvePath(s.root, scope.OrgID, name)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "resolve repository: %v", err)
		}
		if _, err := run(path, "rev-parse", "--verify", "refs/heads/"+branch); err != nil {
			if rollback != nil {
				rollback()
			}
			return nil, status.Errorf(codes.FailedPrecondition, "no branch %q in this repository", branch)
		}
		if _, err := run(path, "symbolic-ref", "HEAD", "refs/heads/"+branch); err != nil {
			if rollback != nil {
				rollback()
			}
			return nil, status.Errorf(codes.Internal, "set default branch: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		if rollback != nil {
			rollback()
		}
		return nil, status.Errorf(codes.Internal, "commit update: %v", err)
	}
	return &gitv1.UpdateRepoResponse{Repo: &gitv1.Repo{
		Id: repo.ID.String(), OrgId: scope.OrgID.String(),
		Name: name, DefaultBranch: branch, Archived: archived,
	}}, nil
}

// TransferRepo moves a repository to another organization.
//
// The caller must administer both sides, so the receiving organization is checked
// against the caller's own scope rather than taken on trust from the request: a
// caller who could name any organization could push a repository into one they do
// not belong to.
func (s *Server) TransferRepo(ctx context.Context, req *gitv1.TransferRepoRequest) (*gitv1.TransferRepoResponse, error) {
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	toOrg, err := uuid.Parse(req.GetToOrg())
	if err != nil || toOrg == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "to_org must be an organization id")
	}
	if toOrg == scope.OrgID {
		return nil, status.Error(codes.InvalidArgument, "the repository is already in this organization")
	}
	repo, err := s.repoByName(ctx, scope.OrgID, req.GetRepo())
	if err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin transfer: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`UPDATE gitplatform.repositories SET org_id = $1 WHERE id = $2 AND org_id = $3`,
		toOrg, repo.ID, scope.OrgID); err != nil {
		return nil, status.Errorf(codes.Internal, "transfer repository: %v", err)
	}
	rollback, err := moveRepoDir(s.root, scope.OrgID, toOrg, repo.Name, repo.Name)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "transfer repository: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		rollback()
		return nil, status.Errorf(codes.Internal, "commit transfer: %v", err)
	}
	return &gitv1.TransferRepoResponse{Repo: &gitv1.Repo{
		Id: repo.ID.String(), OrgId: toOrg.String(),
		Name: repo.Name, DefaultBranch: repo.DefaultBranch, Archived: repo.Archived,
	}}, nil
}

// moveRepoDir moves a repository's directory and returns the function that puts it
// back. It is a free function rather than a method so it holds no state on the
// server, which is shared by every concurrent request.
func moveRepoDir(root string, fromOrg, toOrg uuid.UUID, fromName, toName string) (func(), error) {
	oldPath, err := resolvePath(root, fromOrg, fromName)
	if err != nil {
		return nil, err
	}
	newPath, err := resolvePath(root, toOrg, toName)
	if err != nil {
		return nil, err
	}
	if oldPath == newPath {
		return func() {}, nil
	}
	if _, err := os.Stat(newPath); err == nil {
		return nil, fmt.Errorf("a repository directory already exists at %s", toName)
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
		return nil, fmt.Errorf("create destination directory: %w", err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return nil, fmt.Errorf("move repository directory: %w", err)
	}
	return func() { _ = os.Rename(newPath, oldPath) }, nil
}

// archived reports whether the repository refuses writes.
func (s *Server) archived(ctx context.Context, orgID, id uuid.UUID) (bool, error) {
	var archived bool
	err := s.pool.QueryRow(ctx,
		`SELECT archived FROM gitplatform.repositories WHERE id = $1 AND org_id = $2`,
		id, orgID).Scan(&archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, status.Errorf(codes.NotFound, "no repository %s in this organization", id)
	}
	return archived, err
}
