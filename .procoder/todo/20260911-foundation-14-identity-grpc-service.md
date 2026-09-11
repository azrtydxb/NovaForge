# foundation 14: identity gRPC service

Status: open
Created: 2026-09-11

## Description

Plan step 14 of `.procoder/plans/foundation.md`, which exists to: Stand up the NovaForge backend floor: a deployable Kubernetes stack where a standard git client clones and pushes over HTTPS and SSH against org-isolated, capability-checked repositories, driven entirely through an OpenAPI-described REST edge and the nf CLI.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `proto/identity/v1/identity.proto`, `cmd/identity/main.go`, `internal/identity/grpc.go`, `internal/identity/grpc_test.go`, `buf.gen.yaml`

Interfaces: produces the gRPC service `novaforge.identity.v1.IdentityService` with RPCs `Register`, `Login`, `ResolveSession`, `ResolveToken`, `ResolveFingerprint`, `CreateOrg`, `AddOrgMember`, and `IssueGrant`. The edge in Task 16 and both git transports consume these; no other service touches the identity schema.

## Acceptance criteria

- [ ] Write `proto/identity/v1/identity.proto` with `syntax = "proto3";`, `package novaforge.identity.v1;`, `option go_package = "github.com/novaforge/novaforge/gen/identity/v1;identityv1";` and the eight RPCs above, where `LoginRequest` carries `username`, `password`, and `totp_code`, and `LoginResponse` carries `session_token` and `requires_totp`.
- [ ] Write `buf.gen.yaml` generating Go and gRPC stubs into `gen/`, and add `generate: buf generate` to the Makefile. Run `make generate` and commit the generated code so builds need no codegen step.
- [ ] Write the failing test `internal/identity/grpc_test.go`: `func TestLoginRequiresTOTPWhenEnabled(t *testing.T)` registers a user, enables TOTP, then asserts `Login` without `totp_code` returns `codes.Unauthenticated` and `requires_totp == true`, and that `Login` with a valid code returns a session token; `func TestResolveTokenRejectsRevoked(t *testing.T)` asserts a revoked token yields `codes.Unauthenticated`. Run `go test ./internal/identity/` — expect FAIL with "undefined: identity.NewGRPCServer".
- [ ] Implement `grpc.go` wiring `Store`, `SessionStore`, `TokenStore`, `SSHKeyStore`, and `capability.Store`, mapping domain errors to `status.Error(codes.Unauthenticated, ...)` and `codes.PermissionDenied` for cross-org.
- [ ] Implement `cmd/identity/main.go`: call `version.Require("git","2.40.0")` only if the binary is needed (it is not here), run `database.Migrate(url, "identity", migrationsFS)`, connect Redis, serve gRPC on `IDENTITY_GRPC_PORT` (default 9091), and shut down on SIGINT/SIGTERM with a 30s drain.
- [ ] Run `go test ./internal/identity/` — expect PASS.
- [ ] Commit as `feat: add identity grpc service`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
