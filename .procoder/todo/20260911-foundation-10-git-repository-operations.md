# foundation 10: Git repository operations

Status: open
Created: 2026-09-11

## Description

Plan step 10 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/gitops/repo.go`, `internal/gitops/repo_test.go`

Interfaces: produces `gitops.Repo` with `gitops.Init(root string, orgID uuid.UUID, name string) (Repo, error)`, `gitops.Open(root string, orgID uuid.UUID, name string) (Repo, error)`, and methods `(Repo) Path() string`, `(Repo) Branches() ([]Ref, error)`, `(Repo) Tags() ([]Ref, error)`, `(Repo) Log(ref string, limit int) ([]Commit, error)`, `(Repo) Tree(ref, path string) ([]TreeEntry, error)`, `(Repo) Blob(ref, path string) ([]byte, error)`, and `(Repo) Diff(from, to string) (string, error)`. `Ref` is `{Name, SHA, Kind string}`; `Commit` is `{SHA, Message, AuthorName, AuthorEmail string, At time.Time}`; `TreeEntry` is `{Mode, Kind, SHA, Name string, Size int64}`.

## Acceptance criteria

- [ ] Write the failing test `internal/gitops/repo_test.go`: `func TestInitAndBranches(t *testing.T)` inits a repo in `t.TempDir()`, shells `git --git-dir=<path> commit-tree` to create a commit, points `refs/heads/main` at it, and asserts `Branches()` returns exactly one ref named `main`; `func TestPathTraversalRejected(t *testing.T)` asserts `gitops.Open(root, orgID, "../escape")` returns an error containing "invalid repository name". Run `go test ./internal/gitops/` — expect FAIL with "undefined: gitops.Init".
- [ ] Implement a `run(dir string, args ...string) ([]byte, error)` helper wrapping `exec.Command("git", args...)` that captures stderr and returns `fmt.Errorf("git %s: %w — %s", strings.Join(args, " "), err, stderr)`.
- [ ] Implement repository path resolution as `filepath.Join(root, orgID.String(), name+".git")` after validating `name` against `^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$` and rejecting anything else with `fmt.Errorf("invalid repository name %q", name)`. The org id segment is what keeps one org's repositories unreachable from another's.
- [ ] Implement `Init` as `git init --bare --initial-branch=main <path>`.
- [ ] Implement `Branches` and `Tags` with `git for-each-ref --format=%(refname:short)%00%(objectname)%00%(objecttype) refs/heads/` (and `refs/tags/`), splitting on NUL.
- [ ] Implement `Log` with `git log --format=%H%x00%s%x00%an%x00%ae%x00%aI -n <limit> <ref>`, `Tree` with `git ls-tree -l <ref> -- <path>`, `Blob` with `git cat-file blob <ref>:<path>`, and `Diff` with `git diff <from>..<to>`.
- [ ] Run `go test ./internal/gitops/` — expect PASS.
- [ ] Commit as `feat: add git repository operations over the git binary`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
