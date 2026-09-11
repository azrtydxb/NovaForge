# work-ci 06: Runner protocol over a pushed gRPC stream

Status: closed 2026-09-11
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

- [x] Write the failing test `internal/ci/dispatch_test.go`: `func TestDispatchReachesMatchingRunner(t *testing.T)` registers two runners with labels `{"linux"}` and `{"windows"}`, dispatches a job requiring `linux`, and asserts only the first channel receives it; `func TestDispatchNoMatchingRunner(t *testing.T)` asserts dispatching a job whose labels match no runner returns an error containing "no runner"; `func TestBrokenStreamOrphansJob(t *testing.T)` registers a runner, dispatches a job, unregisters the runner without a status report, and asserts the reaper marks the job `failure` with detail containing "runner disconnected". Run `go test ./internal/ci/` — expect FAIL with "undefined: ci.Dispatcher".
- [x] Write `proto/ci/v1/ci.proto` with the three RPCs above, where `JobMessage` carries `job_id`, `run_id`, `repo_clone_url`, `commit_sha`, `run_cmd`, `agent_role`, `image`, and a map of `env`, and `RunnerMessage` carries `heartbeat` and `log_chunk` variants in a `oneof`. Run `make generate` and commit the generated code.
- [x] Implement `Dispatcher` as a mutex-guarded map from runner id to channel plus its label set, selecting the least-recently-dispatched runner whose labels are a superset of the job's.
- [x] Implement the reaper: on `Unregister`, any job still `running` for that runner is set to `failure` with detail `runner disconnected`, so a dead runner never leaves a job running indefinitely.
- [x] Run `go test ./internal/ci/` — expect PASS.
- [x] Commit as `feat: add runner dispatch over pushed grpc streams`.

## Evidence

- Task 6: runners hold a persistent outbound gRPC stream and the platform pushes jobs down it, so a runner needs no inbound reachability. A broken stream marks in-flight jobs failed with 'runner disconnected' rather than leaving them running forever.
- Built by a parallel agent in an isolated worktree under red-green TDD, merged to main, and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge): ok ci 7.880s, ok runner 1.059s, ok retention 3.456s, 0 failures. Real PostgreSQL 16, Redis 7 and MinIO in the kw cluster.
- A real flakiness source was found and fixed, not ignored: a blanket TRUNCATE per test raced against other suites sharing the external Postgres. Tests were rescoped to their own org, run and random bucket, with TRUNCATE reserved for the two that genuinely need exclusive access to the job queue.
- A real SECURITY defect was caught by the commit gate and fixed rather than suppressed: the runner executed the repository's workflow command directly on the runner host. A CI job runs whatever the workflow file says, so that command is attacker-controlled by construction. Jobs now run in their own Kubernetes pod built from the job's image, with the clone performed inside the pod; the host path requires NOVAFORGE_ALLOW_LOCAL_EXEC=1 and a runner outside a cluster without it refuses to start. TestLocalExecutionRefusedByDefault and TestPodExecutorCreatesIsolatedPod pin both halves.
- `go build ./...` and `go vet ./...` exit 0.
- Implementing commits: 87bfa97, 92f9da6, 36b23ed, 13d2b92, 4884078, 6c0bc8b, 58a0df0, b98555b, plus the isolation fix in d783e12.
