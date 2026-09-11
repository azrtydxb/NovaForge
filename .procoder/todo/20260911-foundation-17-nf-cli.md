# foundation 17: nf CLI

Status: open
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

- [ ] Write the failing test `internal/cli/commands_test.go`: `func TestRepoCreateAndList(t *testing.T)` starts a stub edge with `httptest.NewServer`, runs the `repo create` then `repo list` commands against it, and asserts the created repository name appears in stdout; `func TestConfigFilePermissions(t *testing.T)` asserts the written config file's mode is exactly 0600. Run `go test ./internal/cli/` — expect FAIL with "undefined: cli.Execute".
- [ ] Implement `client.go` as a thin REST client sending `Authorization: Bearer <token>` and returning `fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, body)` for non-2xx responses.
- [ ] Implement `commands.go` with `flag.NewFlagSet` per subcommand — no third-party CLI dependency — and `Execute(args []string, stdout, stderr io.Writer) int` returning the process exit code.
- [ ] Run `go test ./internal/cli/` — expect PASS.
- [ ] Commit as `feat: add nf cli`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
