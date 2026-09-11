# foundation 10: Git repository operations

Status: closed 2026-09-11
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

- [x] Write the failing test `internal/gitops/repo_test.go`: `func TestInitAndBranches(t *testing.T)` inits a repo in `t.TempDir()`, shells `git --git-dir=<path> commit-tree` to create a commit, points `refs/heads/main` at it, and asserts `Branches()` returns exactly one ref named `main`; `func TestPathTraversalRejected(t *testing.T)` asserts `gitops.Open(root, orgID, "../escape")` returns an error containing "invalid repository name". Run `go test ./internal/gitops/` — expect FAIL with "undefined: gitops.Init".
- [x] Implement a `run(dir string, args ...string) ([]byte, error)` helper wrapping `exec.Command("git", args...)` that captures stderr and returns `fmt.Errorf("git %s: %w — %s", strings.Join(args, " "), err, stderr)`.
- [x] Implement repository path resolution as `filepath.Join(root, orgID.String(), name+".git")` after validating `name` against `^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$` and rejecting anything else with `fmt.Errorf("invalid repository name %q", name)`. The org id segment is what keeps one org's repositories unreachable from another's.
- [x] Implement `Init` as `git init --bare --initial-branch=main <path>`.
- [x] Implement `Branches` and `Tags` with `git for-each-ref --format=%(refname:short)%00%(objectname)%00%(objecttype) refs/heads/` (and `refs/tags/`), splitting on NUL.
- [x] Implement `Log` with `git log --format=%H%x00%s%x00%an%x00%ae%x00%aI -n <limit> <ref>`, `Tree` with `git ls-tree -l <ref> -- <path>`, `Blob` with `git cat-file blob <ref>:<path>`, and `Diff` with `git diff <from>..<to>`.
- [x] Run `go test ./internal/gitops/` — expect PASS.
- [x] Commit as `feat: add git repository operations over the git binary`.

## Evidence

- Task 10: repository operations by shelling out to the real git binary, with name validation and org-id path segmentation keeping one org's repositories unreachable from another's.
- Built by a parallel agent in an isolated git worktree under strict red-green TDD, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge by the main agent): `go test -count=1 -v ./internal/gitops/... ./internal/capability/... ./internal/events/...` → 19 PASS, 0 FAIL. ok gitops 3.085s, ok capability 1.363s, ok events 0.601s.
- These are REAL git operations, not simulations: TestCloneAndPushOverHTTP and TestPushOverSSH drive an unmodified git 2.50.1 client through a real clone, commit and push. TestPushDeniedByCapability and TestPushOverSSHDeniedByCapability prove both transports refuse an out-of-scope ref identically. TestPathTraversalRejected and TestPrefixEscapeDenied pin the escape cases. TestPushOverHTTPPublishesEvent consumes the push event back out of a real Redis consumer group.
- Datastores are the REAL PostgreSQL 16 and Redis 7 in the kw cluster, not mocks.
- Two documented protocol deviations, both in the commit bodies: push denial is returned as a git-receive-pack report-status "ng <ref> <reason>" inside side-band-64k framing, because git's smart-HTTP client discards a bare non-2xx body and the plan's own test requires the reason to reach stderr; and SSH cannot use --stateless-rpc for the advertisement because git's interactive SSH client blocks waiting for it, so the advertisement is sent first and the push body is then buffered and authorized exactly as HTTP does.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: c83d80c, 9759e95, 2b1dad1, 37553b2, a44d29d. Merged in 67b3c31.
