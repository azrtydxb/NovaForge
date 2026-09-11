# foundation 01: Go module, tooling, and the check command

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 1 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `go.mod`, `Makefile`, `.gitignore`, `internal/version/version.go`, `internal/version/version_test.go`

Interfaces: produces `version.Require(bin string, min string) error` and `version.Current() string`, used by every service's startup path in later tasks.

## Acceptance criteria

- [x] Write the failing test `internal/version/version_test.go`: `func TestRequireRejectsOldGit(t *testing.T) { err := version.Require("git", "2.40.0"); if err != nil && !strings.Contains(err.Error(), "git") { t.Fatalf("want git in error, got %v", err) } }` and `func TestRequireMissingBinary(t *testing.T) { if err := version.Require("definitely-not-a-binary", "1.0.0"); err == nil { t.Fatal("want error for missing binary") } }`. Run `go test ./internal/version/` — expect FAIL with "no required module provides package".
- [x] Create `go.mod` with `module github.com/novaforge/novaforge` and `go 1.26`.
- [x] Implement `internal/version/version.go`: `Require` runs `exec.Command(bin, "--version")`, parses the first three-part version number with the regexp pattern (\d+)\.(\d+)\.(\d+), compares numerically against `min`, and returns `fmt.Errorf("%s %s is older than required %s", bin, got, min)` when short, or `fmt.Errorf("%s not found: %w", bin, err)` when the binary is absent.
- [x] Run `go test ./internal/version/` — expect PASS.
- [x] Write `Makefile` with targets `test: go test ./...`, `build: go build ./...`, `lint: go vet ./...`, and `generate`.
- [x] Run `make test` and `make lint` — expect both to exit 0.
- [x] Commit as `feat: add go module, makefile, and binary version guard`.

## Evidence

- Red: `go test ./internal/version/` before go.mod existed failed with "cannot find main module" — the package could not build.
- go.mod created: module github.com/novaforge/novaforge, go 1.26.
- internal/version/version.go implements Require by exec-ing `<bin> --version`, parsing the first three-part version, comparing numerically, and returning the two documented error strings.
- Green: `go test ./internal/version/` → ok github.com/novaforge/novaforge/internal/version 0.915s. Three tests pass, including TestRequireRejectsTooNewMinimum which exercises the "older than required" path against the real git 2.50.1 on PATH.
- Makefile written with test/build/lint/generate/tidy targets. `make test` → ok; `make lint` (go vet ./...) → exit 0 with no findings.
- Committed as 71c5236 "feat: add go module, makefile, and binary version guard".
