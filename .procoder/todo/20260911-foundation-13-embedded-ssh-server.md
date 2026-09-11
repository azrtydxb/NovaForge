# foundation 13: Embedded SSH server

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 13 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/gitops/ssh.go`, `internal/gitops/ssh_test.go`

Interfaces: produces `gitops.NewSSHServer(root string, hostKey ssh.Signer, lookup FingerprintFunc, caps CapFunc) *SSHServer` with `type FingerprintFunc func(ctx context.Context, fingerprint string) (authz.Scope, error)` and methods `(*SSHServer) Serve(l net.Listener) error` and `(*SSHServer) Addr() string`. It accepts only the exec requests `git-upload-pack '<org>/<repo>.git'` and `git-receive-pack '<org>/<repo>.git'`.

## Acceptance criteria

- [x] Write the failing test `internal/gitops/ssh_test.go`: `func TestCloneOverSSH(t *testing.T)` generates an ed25519 host key and a client key, registers the client fingerprint through `FingerprintFunc`, starts the server on `net.Listen("tcp","127.0.0.1:0")`, and runs `git -c core.sshCommand="ssh -i <key> -o StrictHostKeyChecking=no -p <port>" clone ssh://git@127.0.0.1/<org>/<repo>.git`, asserting exit 0; `func TestUnknownKeyRejected(t *testing.T)` uses an unregistered key and asserts the clone fails with stderr containing "permission denied"; `func TestNonGitCommandRejected(t *testing.T)` opens a session requesting `exec "/bin/sh"` and asserts the channel is closed with a non-zero status. Run `go test ./internal/gitops/` — expect FAIL with "undefined: gitops.NewSSHServer".
- [x] Implement the server with `ssh.NewServerConn` and a `PublicKeyCallback` that computes `ssh.FingerprintSHA256(key)` and delegates to `FingerprintFunc`, rejecting with `fmt.Errorf("permission denied")` when it errors.
- [x] Parse the exec payload with `ssh.Unmarshal` into `struct{ Command string }`, accept only the two git commands via a regexp anchored as ^git-(upload|receive)-pack followed by a single-quoted path, and reject everything else by sending `exit-status` 128.
- [x] Run `CapFunc` for receive-pack before spawning git, exactly as Task 11 does, so both transports refuse identically.
- [x] Run `go test ./internal/gitops/` — expect PASS.
- [x] Commit as `feat: add embedded ssh server for git transport`.

## Evidence

- Task 13: embedded golang.org/x/crypto/ssh server authenticating by SHA256 fingerprint, accepting only the two git exec commands.
- Built by a parallel agent in an isolated git worktree under strict red-green TDD, then merged to main and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge by the main agent): `go test -count=1 -v ./internal/gitops/... ./internal/capability/... ./internal/events/...` → 19 PASS, 0 FAIL. ok gitops 3.085s, ok capability 1.363s, ok events 0.601s.
- These are REAL git operations, not simulations: TestCloneAndPushOverHTTP and TestPushOverSSH drive an unmodified git 2.50.1 client through a real clone, commit and push. TestPushDeniedByCapability and TestPushOverSSHDeniedByCapability prove both transports refuse an out-of-scope ref identically. TestPathTraversalRejected and TestPrefixEscapeDenied pin the escape cases. TestPushOverHTTPPublishesEvent consumes the push event back out of a real Redis consumer group.
- Datastores are the REAL PostgreSQL 16 and Redis 7 in the kw cluster, not mocks.
- Two documented protocol deviations, both in the commit bodies: push denial is returned as a git-receive-pack report-status "ng <ref> <reason>" inside side-band-64k framing, because git's smart-HTTP client discards a bare non-2xx body and the plan's own test requires the reason to reach stderr; and SSH cannot use --stateless-rpc for the advertisement because git's interactive SSH client blocks waiting for it, so the advertisement is sent first and the push body is then buffered and authorized exactly as HTTP does.
- `go build ./...` and `go vet ./...` exit 0 after the merge.
- Implementing commits: c83d80c, 9759e95, 2b1dad1, 37553b2, a44d29d. Merged in 67b3c31.
