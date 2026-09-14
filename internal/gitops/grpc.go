package gitops

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// defaultCommitLimit caps ListCommits when the caller does not specify one.
const defaultCommitLimit = 100

const uniqueViolation = "23505"

// refHeadsPrefix is the "refs/heads/" prefix Merge strips from source_ref
// and target_ref before addressing them in a throwaway clone.
const refHeadsPrefix = "refs/heads/"

// repoRow is one row of the gitplatform.repositories table.
type repoRow struct {
	ID            uuid.UUID
	OrgID         uuid.UUID
	Name          string
	DefaultBranch string
}

// Server implements gitv1.GitServiceServer. Every RPC derives its
// organization from the authz.Scope a server interceptor has already
// resolved and attached to the request context — never from the request
// message, since git.proto's requests carry no org_id field at all.
type Server struct {
	gitv1.UnimplementedGitServiceServer

	pool *pgxpool.Pool
	root string
}

// NewGRPCServer returns a Server storing repository metadata in pool and
// bare repositories under root.
func NewGRPCServer(pool *pgxpool.Pool, root string) *Server {
	return &Server{pool: pool, root: root}
}

func scopeFromContext(ctx context.Context) (authz.Scope, error) {
	scope, err := authz.FromContext(ctx)
	if err != nil || scope.OrgID == uuid.Nil {
		return authz.Scope{}, status.Error(codes.Unauthenticated, "authentication required")
	}
	return scope, nil
}

// CreateRepo creates repository metadata and the on-disk bare repository
// for the caller's organization.
func (s *Server) CreateRepo(ctx context.Context, req *gitv1.CreateRepoRequest) (*gitv1.CreateRepoResponse, error) {
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	id := uuid.New()
	const insert = `
		INSERT INTO gitplatform.repositories (id, org_id, name, default_branch)
		VALUES ($1, $2, $3, 'main')`
	if _, err := s.pool.Exec(ctx, insert, id, scope.OrgID, req.GetName()); err != nil {
		var pgErr *pgconn.PgError
		if isUniqueViolation(err, &pgErr) {
			return nil, status.Errorf(codes.AlreadyExists, "repository %q already exists", req.GetName())
		}
		return nil, status.Errorf(codes.Internal, "create repository record: %v", err)
	}

	if _, err := Init(s.root, scope.OrgID, req.GetName()); err != nil {
		// Best-effort cleanup of the metadata row so a failed disk init
		// doesn't leave a dangling repository that can never be created.
		_, _ = s.pool.Exec(ctx, `DELETE FROM gitplatform.repositories WHERE id = $1`, id)
		return nil, status.Errorf(codes.Internal, "initialise bare repository: %v", err)
	}

	return &gitv1.CreateRepoResponse{Repo: &gitv1.Repo{
		Id:            id.String(),
		OrgId:         scope.OrgID.String(),
		Name:          req.GetName(),
		DefaultBranch: "main",
	}}, nil
}

// GetRepo looks up a repository by name within the caller's organization.
func (s *Server) GetRepo(ctx context.Context, req *gitv1.GetRepoRequest) (*gitv1.GetRepoResponse, error) {
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.repoByName(ctx, scope.OrgID, req.GetName())
	if err != nil {
		return nil, err
	}
	return &gitv1.GetRepoResponse{Repo: toProtoRepo(row)}, nil
}

// ListRepos lists every repository in the caller's organization. A scope
// for one organization never sees another organization's repositories,
// since the query is always predicated on scope.OrgID from context.
func (s *Server) ListRepos(ctx context.Context, req *gitv1.ListReposRequest) (*gitv1.ListReposResponse, error) {
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, org_id, name, default_branch FROM gitplatform.repositories WHERE org_id = $1 ORDER BY name`,
		scope.OrgID,
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list repositories: %v", err)
	}
	defer rows.Close()

	var repos []*gitv1.Repo
	for rows.Next() {
		var r repoRow
		if err := rows.Scan(&r.ID, &r.OrgID, &r.Name, &r.DefaultBranch); err != nil {
			return nil, status.Errorf(codes.Internal, "scan repository: %v", err)
		}
		repos = append(repos, toProtoRepo(r))
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "list repositories: %v", err)
	}
	return &gitv1.ListReposResponse{Repos: repos}, nil
}

// DeleteRepo removes a repository's metadata and its on-disk bare
// repository. Only an owner or admin of the organization may do it: it
// destroys the repository's whole history, and it used to be allowed to any
// caller in the organization — any member, and any agent or platform service
// holding an org-scoped token.
//
// The bare repository is first renamed aside, then the row is deleted, then
// the renamed directory is removed. A failure before the row is gone renames
// the directory back, so the repository is either fully present or fully
// gone — never a record pointing at a missing directory, or a leftover
// directory that makes re-creating the name fail. Nothing is published: no
// consumer subscribes to a repository's deletion, and the rows other
// services key on this repository's id are unreachable once its name no
// longer resolves.
func (s *Server) DeleteRepo(ctx context.Context, req *gitv1.DeleteRepoRequest) (*gitv1.DeleteRepoResponse, error) {
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !scope.IsOrgAdmin() {
		return nil, status.Error(codes.PermissionDenied, "only an organization owner or admin may delete a repository")
	}
	row, err := s.repoByName(ctx, scope.OrgID, req.GetName())
	if err != nil {
		return nil, err
	}
	repo, err := Open(s.root, scope.OrgID, row.Name)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve repository path: %v", err)
	}

	tombstone := repo.Path() + ".deleting-" + uuid.NewString()
	moved := true
	if err := os.Rename(repo.Path(), tombstone); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, status.Errorf(codes.Internal, "move repository aside: %v", err)
		}
		// A record whose directory is already gone is still deletable: that
		// is exactly the broken state this operation should be able to clear.
		moved = false
	}
	restore := func() {
		if moved {
			_ = os.Rename(tombstone, repo.Path())
		}
	}

	tag, err := s.pool.Exec(ctx,
		`DELETE FROM gitplatform.repositories WHERE id = $1 AND org_id = $2`, row.ID, scope.OrgID)
	if err != nil {
		restore()
		return nil, status.Errorf(codes.Internal, "delete repository record: %v", err)
	}
	if tag.RowsAffected() == 0 {
		restore()
		return nil, status.Errorf(codes.NotFound, "repository %q not found", req.GetName())
	}

	if moved {
		if err := os.RemoveAll(tombstone); err != nil {
			// The repository is deleted — its record is gone and the name is
			// free, because the directory no longer sits at the repository's
			// path. Only disk space is left behind, so this is not a failure
			// the caller can act on.
			log.Printf("gitops: repository %s deleted, but removing %s failed: %v", row.ID, tombstone, err)
		}
	}
	return &gitv1.DeleteRepoResponse{Ok: true}, nil
}

// ListBranches lists a repository's branches.
func (s *Server) ListBranches(ctx context.Context, req *gitv1.ListBranchesRequest) (*gitv1.ListBranchesResponse, error) {
	repo, err := s.openScopedRepo(ctx, req.GetRepo())
	if err != nil {
		return nil, err
	}
	refs, err := repo.Branches()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list branches: %v", err)
	}
	return &gitv1.ListBranchesResponse{Refs: toProtoRefs(refs)}, nil
}

// ListTags lists a repository's tags.
func (s *Server) ListTags(ctx context.Context, req *gitv1.ListTagsRequest) (*gitv1.ListTagsResponse, error) {
	repo, err := s.openScopedRepo(ctx, req.GetRepo())
	if err != nil {
		return nil, err
	}
	refs, err := repo.Tags()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list tags: %v", err)
	}
	return &gitv1.ListTagsResponse{Refs: toProtoRefs(refs)}, nil
}

// ListCommits lists commits reachable from ref, most recent first.
func (s *Server) ListCommits(ctx context.Context, req *gitv1.ListCommitsRequest) (*gitv1.ListCommitsResponse, error) {
	repo, err := s.openScopedRepo(ctx, req.GetRepo())
	if err != nil {
		return nil, err
	}
	ref := req.GetRef()
	if ref == "" {
		ref = "HEAD"
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultCommitLimit
	}
	commits, err := repo.Log(ref, limit)
	if err != nil {
		if isGitNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "unknown ref %q", ref)
		}
		return nil, status.Errorf(codes.Internal, "list commits: %v", err)
	}
	return &gitv1.ListCommitsResponse{Commits: toProtoCommits(commits)}, nil
}

// GetTree lists the entries at path within ref.
func (s *Server) GetTree(ctx context.Context, req *gitv1.GetTreeRequest) (*gitv1.GetTreeResponse, error) {
	repo, err := s.openScopedRepo(ctx, req.GetRepo())
	if err != nil {
		return nil, err
	}
	ref := req.GetRef()
	if ref == "" {
		ref = "HEAD"
	}
	entries, err := repo.Tree(ref, req.GetPath())
	if err != nil {
		if isGitNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "unknown ref or path %q", req.GetPath())
		}
		return nil, status.Errorf(codes.Internal, "get tree: %v", err)
	}
	return &gitv1.GetTreeResponse{Entries: toProtoTreeEntries(entries)}, nil
}

// GetBlob returns the contents of the file at path within ref.
func (s *Server) GetBlob(ctx context.Context, req *gitv1.GetBlobRequest) (*gitv1.GetBlobResponse, error) {
	repo, err := s.openScopedRepo(ctx, req.GetRepo())
	if err != nil {
		return nil, err
	}
	ref := req.GetRef()
	if ref == "" {
		ref = "HEAD"
	}
	content, err := repo.Blob(ref, req.GetPath())
	if err != nil {
		if isGitNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "unknown path %q at ref %q", req.GetPath(), ref)
		}
		return nil, status.Errorf(codes.Internal, "get blob: %v", err)
	}
	return &gitv1.GetBlobResponse{Content: content}, nil
}

// GetDiff returns the unified diff between two refs.
func (s *Server) GetDiff(ctx context.Context, req *gitv1.GetDiffRequest) (*gitv1.GetDiffResponse, error) {
	repo, err := s.openScopedRepo(ctx, req.GetRepo())
	if err != nil {
		return nil, err
	}
	diff := repo.Diff
	if req.GetMergeBase() {
		diff = repo.DiffMergeBase
	}
	unified, err := diff(req.GetFrom(), req.GetTo())
	if err != nil {
		if isGitNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "unknown ref %q or %q", req.GetFrom(), req.GetTo())
		}
		return nil, status.Errorf(codes.Internal, "get diff: %v", err)
	}
	return &gitv1.GetDiffResponse{Unified: unified}, nil
}

// Merge merges source_ref into target_ref using method ("merge" for an
// ordinary merge commit, "squash" for a squash merge, or "ff-only" to
// require a fast-forward), returning the resulting commit sha. Since the
// stored repository is bare, the merge is performed in a throwaway clone
// that is pushed back and discarded.
func (s *Server) Merge(ctx context.Context, req *gitv1.MergeRequest) (*gitv1.MergeResponse, error) {
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.repoByName(ctx, scope.OrgID, req.GetRepo())
	if err != nil {
		return nil, err
	}
	repo, err := Open(s.root, scope.OrgID, row.Name)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve repository path: %v", err)
	}
	if req.GetSourceRef() == "" || req.GetTargetRef() == "" {
		return nil, status.Error(codes.InvalidArgument, "source_ref and target_ref are required")
	}

	sha, err := mergeRefs(repo.Path(), req.GetSourceRef(), req.GetTargetRef(), req.GetMethod(), req.GetMessage())
	if err != nil {
		if isGitNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "unknown ref %q or %q", req.GetSourceRef(), req.GetTargetRef())
		}
		if isMergeConflict(err) {
			return nil, status.Errorf(codes.FailedPrecondition, "merge conflict: %v", err)
		}
		return nil, status.Errorf(codes.Internal, "merge: %v", err)
	}
	return &gitv1.MergeResponse{MergeSha: sha}, nil
}

// repoByName looks up a repository by name scoped to orgID, returning
// codes.NotFound when it does not exist in that organization.
// repoByName resolves a repository from either its name or its id.
//
// People and URLs name repositories; platform workers carry ids — the CI
// scheduler knows only the id from the push event it is handling. Accepting
// one spelling meant the scheduler's reads returned NotFound and every CI run
// silently did nothing. Resolving both here keeps the ownership of repository
// identity in the service that owns it, exactly as organizations are resolved.
func (s *Server) repoByName(ctx context.Context, orgID uuid.UUID, ref string) (repoRow, error) {
	if ref == "" {
		return repoRow{}, status.Error(codes.InvalidArgument, "repo is required")
	}
	column, value := "name", any(ref)
	if id, err := uuid.Parse(ref); err == nil {
		column, value = "id", any(id)
	}
	var r repoRow
	err := s.pool.QueryRow(ctx,
		`SELECT id, org_id, name, default_branch FROM gitplatform.repositories WHERE org_id = $1 AND `+column+` = $2`,
		orgID, value,
	).Scan(&r.ID, &r.OrgID, &r.Name, &r.DefaultBranch)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return repoRow{}, status.Errorf(codes.NotFound, "repository %q not found", ref)
		}
		return repoRow{}, status.Errorf(codes.Internal, "lookup repository: %v", err)
	}
	return r, nil
}

// openScopedRepo resolves req's repo name to a Repo handle, scoped to the
// caller's organization from context.
func (s *Server) openScopedRepo(ctx context.Context, name string) (Repo, error) {
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return Repo{}, err
	}
	row, err := s.repoByName(ctx, scope.OrgID, name)
	if err != nil {
		return Repo{}, err
	}
	repo, err := Open(s.root, scope.OrgID, row.Name)
	if err != nil {
		return Repo{}, status.Errorf(codes.Internal, "resolve repository path: %v", err)
	}
	return repo, nil
}

func toProtoRepo(r repoRow) *gitv1.Repo {
	return &gitv1.Repo{
		Id:            r.ID.String(),
		OrgId:         r.OrgID.String(),
		Name:          r.Name,
		DefaultBranch: r.DefaultBranch,
	}
}

func toProtoRefs(refs []Ref) []*gitv1.Ref {
	out := make([]*gitv1.Ref, 0, len(refs))
	for _, r := range refs {
		out = append(out, &gitv1.Ref{Name: r.Name, Sha: r.SHA, Kind: r.Kind})
	}
	return out
}

func toProtoCommits(commits []Commit) []*gitv1.Commit {
	out := make([]*gitv1.Commit, 0, len(commits))
	for _, c := range commits {
		out = append(out, &gitv1.Commit{
			Sha:         c.SHA,
			Message:     c.Message,
			AuthorName:  c.AuthorName,
			AuthorEmail: c.AuthorEmail,
			At:          c.At.UTC().Format(time.RFC3339),
		})
	}
	return out
}

func toProtoTreeEntries(entries []TreeEntry) []*gitv1.TreeEntry {
	out := make([]*gitv1.TreeEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, &gitv1.TreeEntry{
			Mode: e.Mode,
			Kind: e.Kind,
			Sha:  e.SHA,
			Name: e.Name,
			Size: e.Size,
		})
	}
	return out
}

// isUniqueViolation reports whether err is a Postgres unique_violation,
// populating target when it is.
func isUniqueViolation(err error, target **pgconn.PgError) bool {
	if errors.As(err, target) && (*target).Code == uniqueViolation {
		return true
	}
	return false
}

// isGitNotFound reports whether err (as returned by this package's run())
// looks like git reporting an unknown ref, revision, path, or object,
// rather than some other failure.
func isGitNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	markers := []string{
		"does not exist",
		"unknown revision",
		"bad revision",
		"not a valid object name",
		"not a tree object",
		"Not a valid object name",
		"fatal: bad object",
		"ambiguous argument",
	}
	for _, m := range markers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// isMergeConflict reports whether err looks like a git merge that stopped
// due to conflicting changes, rather than some other failure.
func isMergeConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "CONFLICT") || strings.Contains(msg, "Automatic merge failed") ||
		strings.Contains(msg, "fix conflicts")
}

// mergeRefs merges sourceRef into targetRef in the bare repository at
// repoPath and returns the resulting commit sha. Since repoPath is bare, the
// merge happens in a throwaway clone that is pushed back to repoPath and
// then discarded: 1) clone repoPath to a temp dir, 2) check out targetRef,
// 3) merge sourceRef using the requested method, 4) push the result back
// onto targetRef in repoPath, 5) resolve the resulting sha.
func mergeRefs(repoPath, sourceRef, targetRef, method, message string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "novaforge-merge-*")
	if err != nil {
		return "", fmt.Errorf("create temp clone dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if _, err := run("", "clone", "--no-hardlinks", repoPath, tmpDir); err != nil {
		return "", err
	}

	// A fresh clone only creates a local branch for the default branch
	// (HEAD); every other branch exists solely as a remote-tracking ref
	// under origin/. Both refs are therefore addressed via their
	// remote-tracking form here, and targetRef is (re)created locally from
	// it so the merge result can be committed and pushed back.
	targetBranch := strings.TrimPrefix(targetRef, refHeadsPrefix)
	sourceBranch := strings.TrimPrefix(sourceRef, refHeadsPrefix)
	if _, err := run(tmpDir, "checkout", "-B", targetBranch, "origin/"+targetBranch); err != nil {
		return "", err
	}

	if message == "" {
		message = fmt.Sprintf("Merge %s into %s", sourceRef, targetRef)
	}

	sourceRemoteRef := "origin/" + sourceBranch
	env := []string{"-c", "user.email=novaforge@localhost", "-c", "user.name=NovaForge"}
	switch method {
	case "squash":
		if _, err := runEnv(tmpDir, env, "merge", "--squash", sourceRemoteRef); err != nil {
			return "", err
		}
		if _, err := runEnv(tmpDir, env, "commit", "-m", message); err != nil {
			return "", err
		}
	case "ff-only":
		if _, err := runEnv(tmpDir, env, "merge", "--ff-only", sourceRemoteRef); err != nil {
			return "", err
		}
	default: // "merge" or unset: an ordinary merge commit.
		if _, err := runEnv(tmpDir, env, "merge", "--no-ff", "-m", message, sourceRemoteRef); err != nil {
			return "", err
		}
	}
	targetRef = targetBranch

	if _, err := run(tmpDir, "push", "origin", "HEAD:"+targetRef); err != nil {
		return "", err
	}
	out, err := run(tmpDir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// runEnv is run with extra leading git arguments (such as -c config
// overrides) inserted before the subcommand.
func runEnv(dir string, extraArgs []string, args ...string) ([]byte, error) {
	full := append(append([]string{}, extraArgs...), args...)
	return run(dir, full...)
}
