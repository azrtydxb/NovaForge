# foundation 17: nf CLI

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 17 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `cmd/nf/main.go`, `internal/cli/client.go`, `internal/cli/commands.go`, `internal/cli/commands_test.go`

Interfaces: produces the commands `nf login`, `nf org create`, `nf repo create`, `nf repo list`, `nf repo branches`, and `nf repo log`, all speaking the REST edge from Task 16 and storing the session token in `$XDG_CONFIG_HOME/novaforge/config.json` with mode 0600.

## Acceptance criteria

- [x] Write the failing test `internal/cli/commands_test.go`: `func TestRepoCreateAndList(t *testing.T)` starts a stub edge with `httptest.NewServer`, runs the `repo create` then `repo list` commands against it, and asserts the created repository name appears in stdout; `func TestConfigFilePermissions(t *testing.T)` asserts the written config file's mode is exactly 0600. Run `go test ./internal/cli/` — expect FAIL with "undefined: cli.Execute".
- [x] Implement `client.go` as a thin REST client sending `Authorization: Bearer <token>` and returning `fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, body)` for non-2xx responses.
- [x] Implement `commands.go` with `flag.NewFlagSet` per subcommand — no third-party CLI dependency — and `Execute(args []string, stdout, stderr io.Writer) int` returning the process exit code.
- [x] Run `go test ./internal/cli/` — expect PASS.
- [x] Commit as `feat: add nf cli`.

## Evidence

- Task 17: the nf CLI — login, org, repo, work and run commands, with the config file holding a bearer token at mode 0600.
- Green (unit): `go test -count=1 ./...` passes across every package, against the REAL PostgreSQL 16 + pgvector, Redis 7 and MinIO running in the kw cluster.
- Green (CLUSTER ACCEPTANCE, the evidence that matters): `bash tests/e2e/deploy_test.sh` against the live 8-node ARM64 k3s cluster returned:
  "PASS: NovaForge is deployed on the kw cluster and a real git round trip works."
  Specifically: all six deployments ready; the edge answering /healthz at 192.168.10.128; register, login, org create and repo create through the REST API via the nf CLI; an UNMODIFIED git client cloning over HTTPS from 192.168.10.123:8081, committing and pushing (commit 5f8a6130fd49e55a4a3bc121d6785ef2eec1f89f); and that commit and its branch read back through the REST API.
- Images were built for linux/arm64 on the in-cluster BuildKit over mTLS, pushed to nexus, and pulled by the nodes. No local Docker daemon was involved.
- Four real defects were found by running this against the cluster rather than by inspection, each fixed with a test: the edge resolved a bearer only as a PAT so CLI calls failed; the edge called services anonymously after authenticating; a credential carried no organization so every org-scoped service refused; and the org lookup queried an unqualified table that no test covered.
- `go build ./...` and `go vet ./...` exit 0.
