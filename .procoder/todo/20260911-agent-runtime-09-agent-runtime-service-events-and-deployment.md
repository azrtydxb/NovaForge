# agent-runtime 09: agent-runtime service, events, and deployment

Status: open
Created: 2026-09-11

## Description

Plan step 9 of `.procoder/plans/agent-runtime.md`, which exists to: Make AI agents first-class contributors: agent identities with provenance, Agent Runs executing in isolated per-run Kubernetes namespaces, a typed and fully audited tool surface instead of shell access, and all model access routed through go-ai-sdk so no provider-specific logic enters NovaForge.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `proto/agents/v1/agents.proto`, `cmd/agent-runtime/main.go`, `internal/agents/grpc.go`, `internal/agents/grpc_test.go`, `internal/edge/agent_routes.go`, `api/openapi.yaml`, `Dockerfile.agent-runtime`, `deploy/helm/novaforge/templates/agent-runtime.yaml`, `deploy/helm/novaforge/templates/agent-rbac.yaml`, `tests/e2e/agent_run_test.sh`

Interfaces: produces `novaforge.agents.v1.AgentService` with `CreateAgent`, `ListAgents`, `StartRun`, `GetRun`, `CancelRun`, and `StreamRunEvents`, plus the Redis stream constant `events.StreamAgentEvents = "stream:agent:events"` carrying one message per run state change and per tool call.

## Acceptance criteria

- [ ] Extend `api/openapi.yaml` with `GET|POST /api/v1/orgs/{org}/agents`, `POST /api/v1/orgs/{org}/repos/{repo}/agent-runs`, `GET /api/v1/orgs/{org}/repos/{repo}/agent-runs/{id}`, `GET /api/v1/orgs/{org}/repos/{repo}/agent-runs/{id}/events`, and `GET /api/v1/orgs/{org}/repos/{repo}/agent-runs/{id}/tool-calls`.
- [ ] Write the failing test `internal/agents/grpc_test.go`: `func TestStartRunIssuesScopedGrant(t *testing.T)` starts a run for Work Item `NF-1` and asserts the issued grant's `WriteBranch` is exactly `agents/NF-1/` and that `secrets_prod` and `deploy_prod` are false; `func TestStartRunDeniedWithoutSponsor(t *testing.T)` asserts a start with no human sponsor returns `codes.PermissionDenied`; `func TestStreamRunEventsDeliversStateChanges(t *testing.T)` asserts a state change from queued to running is delivered on the stream. Run `go test ./internal/agents/` — expect FAIL with "undefined: agents.NewGRPCServer".
- [ ] Implement `cmd/agent-runtime/main.go`: migrate schema `agents`, serve gRPC on 9095, start the workspace reaper on a 5m ticker calling `Reap(ctx, time.Hour)`, expose `/healthz`, and drain for 30s on SIGTERM.
- [ ] Write `agent-rbac.yaml` granting the service a Role limited to creating, listing, and deleting namespaces, pods, networkpolicies, and resourcequotas bearing the `novaforge.io/run-id` label, and nothing else.
- [ ] Write the failing test `tests/e2e/agent_run_test.sh`: on the kind cluster, start an agent run against a seeded repository with a stub model endpoint, assert a namespace matching `nf-run-*` appears while the run is active, assert the run reaches `succeeded` within 300s, assert the namespace is gone afterwards, and assert `nf agent-run tool-calls <id>` lists at least one recorded call. Run `bash tests/e2e/agent_run_test.sh` — expect FAIL with "Error: no matching deployment novaforge-agent-runtime".
- [ ] Write the Deployment template and Dockerfile, then run `bash tests/e2e/agent_run_test.sh` — expect PASS.
- [ ] Commit as `feat: add agent-runtime service with isolated runs and event streaming`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
