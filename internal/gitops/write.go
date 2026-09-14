package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	"github.com/novaforge/novaforge/internal/authz"
)

// CreateBranch points a new branch at from_ref, or at the repository's
// default branch when from_ref is empty. Creating a branch through the
// platform rather than through a push is what lets an agent — which has no
// push credential of its own — start work on an isolated ref.
func (s *Server) CreateBranch(ctx context.Context, req *gitv1.CreateBranchRequest) (*gitv1.CreateBranchResponse, error) {
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimPrefix(req.GetName(), refHeadsPrefix)
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	row, err := s.repoByName(ctx, scope.OrgID, req.GetRepo())
	if err != nil {
		return nil, err
	}
	repo, err := Open(s.root, scope.OrgID, row.Name)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve repository path: %v", err)
	}

	from := strings.TrimPrefix(req.GetFromRef(), refHeadsPrefix)
	if from == "" {
		from = row.DefaultBranch
	}

	// A branch that already exists is not silently repointed: that would
	// discard whatever was on it, which is never what a caller asking to
	// create a branch meant.
	if _, err := run(repo.Path(), "rev-parse", "--verify", refHeadsPrefix+name); err == nil {
		return nil, status.Errorf(codes.AlreadyExists, "branch %q already exists", name)
	}

	out, err := run(repo.Path(), "rev-parse", "--verify", from+"^{commit}")
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "unknown ref %q", from)
	}
	sha := strings.TrimSpace(string(out))

	// The zero old value makes the update a compare-and-swap: a branch
	// pushed by someone else between the check above and this write is not
	// overwritten, so the event below can truthfully say it did not exist.
	if _, err := run(repo.Path(), "update-ref", refHeadsPrefix+name, sha, zeroSHA); err != nil {
		return nil, status.Errorf(codes.Internal, "create branch %q: %v", name, err)
	}
	s.publishRefUpdate(ctx, scope, row.Name, RefUpdate{Ref: refHeadsPrefix + name, OldSHA: zeroSHA, NewSHA: sha})
	return &gitv1.CreateBranchResponse{
		Ref: &gitv1.Ref{Name: name, Sha: sha, Kind: "branch"},
	}, nil
}

// CreateCommit writes files onto branch as a single commit. An agent's
// changes reach a repository this way rather than through a push, because
// an agent holds a capability grant against the platform, not a git
// credential — the platform stays the one that decides what may be written.
func (s *Server) CreateCommit(ctx context.Context, req *gitv1.CreateCommitRequest) (*gitv1.CreateCommitResponse, error) {
	scope, err := scopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if len(req.GetFiles()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one file change is required")
	}
	if req.GetMessage() == "" {
		return nil, status.Error(codes.InvalidArgument, "message is required")
	}
	// Every path is validated before any clone or write happens, so a
	// refused path is reported as the caller's error rather than surfacing
	// as an internal failure from part-way through the commit.
	for _, f := range req.GetFiles() {
		if _, err := safeRepoPath(f.GetPath()); err != nil {
			return nil, err
		}
	}
	row, err := s.repoByName(ctx, scope.OrgID, req.GetRepo())
	if err != nil {
		return nil, err
	}
	repo, err := Open(s.root, scope.OrgID, row.Name)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve repository path: %v", err)
	}

	branch := strings.TrimPrefix(req.GetBranch(), refHeadsPrefix)
	if branch == "" {
		branch = row.DefaultBranch
	}

	old, sha, err := commitFiles(repo.Path(), branch, req)
	if err != nil {
		if isGitNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "unknown branch %q", branch)
		}
		return nil, status.Errorf(codes.Internal, "commit to %q: %v", branch, err)
	}
	s.publishRefUpdate(ctx, scope, row.Name, RefUpdate{Ref: refHeadsPrefix + branch, OldSHA: old, NewSHA: sha})
	return &gitv1.CreateCommitResponse{Sha: sha}, nil
}

// commitFiles applies req's file changes to branch in a throwaway clone of
// the bare repository and pushes the result back, which is the same shape
// mergeRefs uses: the bare repository is never mutated in place, so a
// failure part-way through leaves it exactly as it was. It returns the
// branch's head before the commit (the zero sha when the commit creates the
// branch) and the new commit's sha, which is what a push event carries.
func commitFiles(repoPath, branch string, req *gitv1.CreateCommitRequest) (oldSHA, newSHA string, err error) {
	tmpDir, err := os.MkdirTemp("", "novaforge-commit-*")
	if err != nil {
		return "", "", fmt.Errorf("create temp clone dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if _, err := run("", "clone", "--no-hardlinks", repoPath, tmpDir); err != nil {
		return "", "", err
	}
	// An empty repository has no branch to check out yet; committing onto
	// it creates the branch, which is what a first commit should do.
	oldSHA = zeroSHA
	if out, err := run(tmpDir, "rev-parse", "--verify", "origin/"+branch); err == nil {
		oldSHA = strings.TrimSpace(string(out))
		if _, err := run(tmpDir, "checkout", "-B", branch, "origin/"+branch); err != nil {
			return "", "", err
		}
	} else if _, err := run(tmpDir, "checkout", "-B", branch); err != nil {
		return "", "", err
	}

	for _, f := range req.GetFiles() {
		clean, err := safeRepoPath(f.GetPath())
		if err != nil {
			return "", "", err
		}
		full := filepath.Join(tmpDir, clean)
		if f.GetDeleted() {
			if _, err := run(tmpDir, "rm", "-f", "--ignore-unmatch", clean); err != nil {
				return "", "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", "", fmt.Errorf("create directory for %s: %w", clean, err)
		}
		if err := os.WriteFile(full, f.GetContent(), 0o644); err != nil {
			return "", "", fmt.Errorf("write %s: %w", clean, err)
		}
		if _, err := run(tmpDir, "add", "--", clean); err != nil {
			return "", "", err
		}
	}

	name := req.GetAuthorName()
	if name == "" {
		name = "NovaForge"
	}
	email := req.GetAuthorEmail()
	if email == "" {
		email = "novaforge@localhost"
	}
	env := []string{"-c", "user.name=" + name, "-c", "user.email=" + email}

	// An agent that produced no net change must not leave an empty commit
	// behind claiming it did work.
	if _, err := run(tmpDir, "diff", "--cached", "--quiet"); err == nil {
		return "", "", fmt.Errorf("the requested changes leave the tree unchanged, so there is nothing to commit")
	}
	if _, err := runEnv(tmpDir, env, "commit", "-m", req.GetMessage()); err != nil {
		return "", "", err
	}
	if _, err := run(tmpDir, "push", leaseFor(branch, oldSHA), "origin", "HEAD:"+refHeadsPrefix+branch); err != nil {
		return "", "", err
	}
	out, err := run(tmpDir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	return oldSHA, strings.TrimSpace(string(out)), nil
}

// zeroSHA is git's value for a ref that does not exist, which a push event
// reports as the old value of a ref the push created.
const zeroSHA = "0000000000000000000000000000000000000000"

// leaseFor makes pushing branch back a compare-and-swap against the head the
// throwaway clone started from. Without it, a push that landed in between
// could be the ref's real previous value while the event reported the
// clone's, and the indexer would diff from a commit that was never the
// branch's head. An all-zero expectation means "must not exist yet".
func leaseFor(branch, expected string) string {
	if strings.Trim(expected, "0") == "" {
		expected = ""
	}
	return "--force-with-lease=" + refHeadsPrefix + branch + ":" + expected
}

// publishRefUpdate tells every push consumer — the indexer and the CI
// scheduler — about a ref the platform itself moved. Merges, commits and
// branch creations made through this service used to move refs silently: the
// code index kept describing a branch as it was before the merge, and CI
// never ran on what an agent committed. They are pushes in every way that
// matters to a consumer, so they go through the very publisher a git push
// uses.
func (s *Server) publishRefUpdate(ctx context.Context, scope authz.Scope, repo string, update RefUpdate) {
	if PushPublisher == nil {
		return
	}
	PushPublisher(context.WithoutCancel(ctx), scope, scope.OrgID, repo, []RefUpdate{update})
}

// safeRepoPath rejects a path that would escape the working tree or touch
// the repository's own .git directory. A commit's paths come from an agent,
// so "../../etc/passwd" and ".git/hooks/pre-commit" are inputs that must be
// refused, not merely unlikely.
func safeRepoPath(p string) (string, error) {
	if p == "" {
		return "", status.Error(codes.InvalidArgument, "a file change needs a path")
	}
	clean := filepath.Clean(p)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", status.Errorf(codes.InvalidArgument, "path %q escapes the repository", p)
	}
	for _, seg := range strings.Split(clean, string(filepath.Separator)) {
		if seg == ".git" {
			return "", status.Errorf(codes.InvalidArgument, "path %q writes into the repository's own git directory", p)
		}
	}
	return clean, nil
}
