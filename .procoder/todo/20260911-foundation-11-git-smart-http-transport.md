# foundation 11: Git smart-HTTP transport

Status: open
Created: 2026-09-11

## Description

Plan step 11 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/gitops/http.go`, `internal/gitops/http_test.go`

Interfaces: produces `gitops.NewHTTPHandler(root string, auth AuthFunc, caps CapFunc) http.Handler` where `type AuthFunc func(ctx context.Context, user, pass string) (authz.Scope, error)` and `type CapFunc func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error`. It serves `GET /{org}/{repo}.git/info/refs`, `POST /{org}/{repo}.git/git-upload-pack`, and `POST /{org}/{repo}.git/git-receive-pack`.

## Acceptance criteria

- [ ] Write the failing test `internal/gitops/http_test.go`: `func TestCloneAndPushOverHTTP(t *testing.T)` starts the handler on `httptest.NewServer`, runs `git clone http://user:token@127.0.0.1:PORT/<org>/<repo>.git` into `t.TempDir()`, commits a file, runs `git push origin main`, then asserts `Log("main", 10)` on the server repo contains the pushed message; `func TestPushDeniedByCapability(t *testing.T)` wires a `CapFunc` that returns an error for `refs/heads/main` and asserts the push fails with a non-zero exit and stderr containing "not permitted". Run `go test ./internal/gitops/` — expect FAIL with "undefined: gitops.NewHTTPHandler".
- [ ] Implement `info/refs`: require `?service=git-upload-pack` or `git-receive-pack`, set `Content-Type: application/x-<service>-advertisement`, write the pkt-line banner `# service=<service>\n` followed by a flush packet `0000`, then stream `git <service> --stateless-rpc --advertise-refs <path>`.
- [ ] Implement the two RPC endpoints streaming the request body into `git <service> --stateless-rpc <path>` and the command's stdout back to the response, handling `Content-Encoding: gzip` request bodies.
- [ ] Enforce authorization before any git process starts: HTTP Basic credentials go through `AuthFunc`, and for `git-receive-pack` parse the requested ref updates from the pkt-line stream and pass them to `CapFunc`, returning 403 when it errors.
- [ ] Run `go test ./internal/gitops/` — expect PASS.
- [ ] Commit as `feat: add git smart-http transport with capability-checked pushes`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
