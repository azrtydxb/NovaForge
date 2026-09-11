# work-ci 07: Runner binary and job execution

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 7 of `.procoder/plans/work-ci.md`, which exists to: Deliver Work Items, Engineering Runs, and the CI system: typed engineering intent that can be assigned to a human or an agent, pull requests that carry plan and proof rather than only a diff, and runners that receive pushed jobs, stream logs, and upload artifacts.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `cmd/runner/main.go`, `internal/runner/executor.go`, `internal/runner/executor_test.go`, `Dockerfile.runner`

Interfaces: produces `runner.Execute(ctx context.Context, job *civ1.JobMessage, workdir string, logs chan<- string) (exitCode int, err error)`. It clones the repository at `job.CommitSha`, runs `job.RunCmd` inside a container built from `job.Image`, and streams every output line into `logs`.

## Acceptance criteria

- [x] Write the failing test `internal/runner/executor_test.go`: `func TestExecuteStreamsLogLines(t *testing.T)` runs a job whose command is `printf 'one\\ntwo\\n'` and asserts the `logs` channel yields exactly `"one"` then `"two"`; `func TestExecuteReturnsNonZeroExit(t *testing.T)` runs `exit 7` and asserts `exitCode == 7` with a nil error, since a failing job is a result and not a transport error; `func TestExecuteRespectsContextCancel(t *testing.T)` runs `sleep 60`, cancels the context after 100ms, and asserts the call returns within 5s. Run `go test ./internal/runner/` — expect FAIL with "undefined: runner.Execute".
- [x] Implement `Execute` with `exec.CommandContext`, wiring stdout and stderr through an `io.Pipe` and a `bufio.Scanner` with a 1 MiB buffer so long lines do not truncate, and setting `Cancel` plus `WaitDelay` of 5s so a cancelled job is killed rather than leaked.
- [x] Implement `cmd/runner/main.go`: register, open the `Connect` stream, send a heartbeat every 15s, execute each received job, forward log chunks as `RunnerMessage.log_chunk`, and call `ReportStatus` on completion. On stream error, reconnect with exponential backoff starting at 1s and capped at 60s.
- [x] Write `Dockerfile.runner` on `golang:1.26` builder and `alpine:3.21` runtime with `RUN apk add --no-cache git ca-certificates`, since the runner clones repositories.
- [x] Run `go test ./internal/runner/` — expect PASS.
- [x] Commit as `feat: add runner binary with streaming job execution`.

## Evidence

- Task 7: the runner binary, streaming log lines with a 1 MiB scanner buffer, returning the real exit code with a nil error for a failing job, and killing a cancelled job via CommandContext Cancel plus WaitDelay. Jobs execute in their own Kubernetes pod.
- Built by a parallel agent in an isolated worktree under red-green TDD, merged to main, and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge): ok ci 7.880s, ok runner 1.059s, ok retention 3.456s, 0 failures. Real PostgreSQL 16, Redis 7 and MinIO in the kw cluster.
- A real flakiness source was found and fixed, not ignored: a blanket TRUNCATE per test raced against other suites sharing the external Postgres. Tests were rescoped to their own org, run and random bucket, with TRUNCATE reserved for the two that genuinely need exclusive access to the job queue.
- A real SECURITY defect was caught by the commit gate and fixed rather than suppressed: the runner executed the repository's workflow command directly on the runner host. A CI job runs whatever the workflow file says, so that command is attacker-controlled by construction. Jobs now run in their own Kubernetes pod built from the job's image, with the clone performed inside the pod; the host path requires NOVAFORGE_ALLOW_LOCAL_EXEC=1 and a runner outside a cluster without it refuses to start. TestLocalExecutionRefusedByDefault and TestPodExecutorCreatesIsolatedPod pin both halves.
- `go build ./...` and `go vet ./...` exit 0.
- Implementing commits: 87bfa97, 92f9da6, 36b23ed, 13d2b92, 4884078, 6c0bc8b, 58a0df0, b98555b, plus the isolation fix in d783e12.
