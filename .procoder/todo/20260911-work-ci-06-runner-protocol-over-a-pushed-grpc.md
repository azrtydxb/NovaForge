# work-ci 06: Runner protocol over a pushed gRPC stream

Status: open
Created: 2026-09-11

## Description

Plan step 6 of `.procoder/plans/work-ci.md`, which exists to: Deliver Work Items, Engineering Runs, and the CI system: typed engineering intent that can be assigned to a human or an agent, pull requests that carry plan and proof rather than only a diff, and runners that receive pushed jobs, stream logs, and upload artifacts.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `proto/ci/v1/ci.proto`, `internal/ci/grpc.go`, `internal/ci/dispatch.go`, `internal/ci/dispatch_test.go`

Interfaces: produces `novaforge.ci.v1.RunnerService` with `rpc Register(RegisterRequest) returns (RegisterResponse)`, `rpc Connect(stream RunnerMessage) returns (stream JobMessage)`, and `rpc ReportStatus(StatusRequest) returns (StatusResponse)`; plus `ci.Dispatcher` with `Register(runnerID uuid.UUID, ch chan<- *civ1.JobMessage)`, `Unregister(runnerID uuid.UUID)`, and `Dispatch(ctx context.Context, job Job, labels []string) error`. The runner binary in Task 7 is the only client.

## Acceptance criteria

- [ ] Write the failing test `internal/ci/dispatch_test.go`: `func TestDispatchReachesMatchingRunner(t *testing.T)` registers two runners with labels `{"linux"}` and `{"windows"}`, dispatches a job requiring `linux`, and asserts only the first channel receives it; `func TestDispatchNoMatchingRunner(t *testing.T)` asserts dispatching a job whose labels match no runner returns an error containing "no runner"; `func TestBrokenStreamOrphansJob(t *testing.T)` registers a runner, dispatches a job, unregisters the runner without a status report, and asserts the reaper marks the job `failure` with detail containing "runner disconnected". Run `go test ./internal/ci/` — expect FAIL with "undefined: ci.Dispatcher".
- [ ] Write `proto/ci/v1/ci.proto` with the three RPCs above, where `JobMessage` carries `job_id`, `run_id`, `repo_clone_url`, `commit_sha`, `run_cmd`, `agent_role`, `image`, and a map of `env`, and `RunnerMessage` carries `heartbeat` and `log_chunk` variants in a `oneof`. Run `make generate` and commit the generated code.
- [ ] Implement `Dispatcher` as a mutex-guarded map from runner id to channel plus its label set, selecting the least-recently-dispatched runner whose labels are a superset of the job's.
- [ ] Implement the reaper: on `Unregister`, any job still `running` for that runner is set to `failure` with detail `runner disconnected`, so a dead runner never leaves a job running indefinitely.
- [ ] Run `go test ./internal/ci/` — expect PASS.
- [ ] Commit as `feat: add runner dispatch over pushed grpc streams`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
