# work-ci 07: Runner binary and job execution

Status: open
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

- [ ] Write the failing test `internal/runner/executor_test.go`: `func TestExecuteStreamsLogLines(t *testing.T)` runs a job whose command is `printf 'one\\ntwo\\n'` and asserts the `logs` channel yields exactly `"one"` then `"two"`; `func TestExecuteReturnsNonZeroExit(t *testing.T)` runs `exit 7` and asserts `exitCode == 7` with a nil error, since a failing job is a result and not a transport error; `func TestExecuteRespectsContextCancel(t *testing.T)` runs `sleep 60`, cancels the context after 100ms, and asserts the call returns within 5s. Run `go test ./internal/runner/` — expect FAIL with "undefined: runner.Execute".
- [ ] Implement `Execute` with `exec.CommandContext`, wiring stdout and stderr through an `io.Pipe` and a `bufio.Scanner` with a 1 MiB buffer so long lines do not truncate, and setting `Cancel` plus `WaitDelay` of 5s so a cancelled job is killed rather than leaked.
- [ ] Implement `cmd/runner/main.go`: register, open the `Connect` stream, send a heartbeat every 15s, execute each received job, forward log chunks as `RunnerMessage.log_chunk`, and call `ReportStatus` on completion. On stream error, reconnect with exponential backoff starting at 1s and capped at 60s.
- [ ] Write `Dockerfile.runner` on `golang:1.26` builder and `alpine:3.21` runtime with `RUN apk add --no-cache git ca-certificates`, since the runner clones repositories.
- [ ] Run `go test ./internal/runner/` — expect PASS.
- [ ] Commit as `feat: add runner binary with streaming job execution`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
