# foundation 13: Embedded SSH server

Status: open
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

- [ ] Write the failing test `internal/gitops/ssh_test.go`: `func TestCloneOverSSH(t *testing.T)` generates an ed25519 host key and a client key, registers the client fingerprint through `FingerprintFunc`, starts the server on `net.Listen("tcp","127.0.0.1:0")`, and runs `git -c core.sshCommand="ssh -i <key> -o StrictHostKeyChecking=no -p <port>" clone ssh://git@127.0.0.1/<org>/<repo>.git`, asserting exit 0; `func TestUnknownKeyRejected(t *testing.T)` uses an unregistered key and asserts the clone fails with stderr containing "permission denied"; `func TestNonGitCommandRejected(t *testing.T)` opens a session requesting `exec "/bin/sh"` and asserts the channel is closed with a non-zero status. Run `go test ./internal/gitops/` — expect FAIL with "undefined: gitops.NewSSHServer".
- [ ] Implement the server with `ssh.NewServerConn` and a `PublicKeyCallback` that computes `ssh.FingerprintSHA256(key)` and delegates to `FingerprintFunc`, rejecting with `fmt.Errorf("permission denied")` when it errors.
- [ ] Parse the exec payload with `ssh.Unmarshal` into `struct{ Command string }`, accept only the two git commands via a regexp anchored as ^git-(upload|receive)-pack followed by a single-quoted path, and reject everything else by sending `exit-status` 128.
- [ ] Run `CapFunc` for receive-pack before spawning git, exactly as Task 11 does, so both transports refuse identically.
- [ ] Run `go test ./internal/gitops/` — expect PASS.
- [ ] Commit as `feat: add embedded ssh server for git transport`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
