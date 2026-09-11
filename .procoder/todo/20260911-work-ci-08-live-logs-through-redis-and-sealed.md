# work-ci 08: Live logs through Redis and sealed logs in object storage

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 8 of `.procoder/plans/work-ci.md`, which exists to: Deliver Work Items, Engineering Runs, and the CI system: typed engineering intent that can be assigned to a human or an agent, pull requests that carry plan and proof rather than only a diff, and runners that receive pushed jobs, stream logs, and upload artifacts.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/ci/logs.go`, `internal/ci/logs_test.go`, `internal/blobstore/s3.go`, `internal/blobstore/s3_test.go`

Interfaces: produces `blobstore.Client` with `Put(ctx, key string, r io.Reader, size int64, contentType string) error`, `Get(ctx, key string) (io.ReadCloser, error)`, and `Delete(ctx, key string) error`; plus `ci.LogSink.Append(ctx, jobID uuid.UUID, line string) error`, `ci.LogSink.Tail(ctx, jobID uuid.UUID, from string) (<-chan string, error)`, and `ci.LogSink.Seal(ctx, jobID uuid.UUID) (objectKey string, err error)`.

## Acceptance criteria

- [x] Write the failing test `internal/blobstore/s3_test.go`: `func TestPutGetRoundTrip(t *testing.T)` skips unless `TEST_S3_ENDPOINT` is set, writes `"hello"` and asserts `Get` returns it; `func TestGetMissingKey(t *testing.T)` asserts a missing key returns an error satisfying `errors.Is(err, blobstore.ErrNotFound)`. Run `go test ./internal/blobstore/` — expect FAIL with "undefined: blobstore.Client".
- [x] Add `go get github.com/minio/minio-go/v7` and implement `Client` against it, configured by `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, and `S3_BUCKET`, creating the bucket when absent so an air-gapped first boot needs no manual setup.
- [x] Write the failing test `internal/ci/logs_test.go`: `func TestTailReceivesAppendedLines(t *testing.T)` starts a tail, appends three lines, and asserts all three arrive in order; `func TestSealMovesLogToObjectStorage(t *testing.T)` appends two lines, seals, asserts the returned key is fetchable from the blobstore with both lines, and asserts the Redis stream key no longer exists. Run `go test ./internal/ci/` — expect FAIL with "undefined: ci.LogSink".
- [x] Implement `Append` as `XADD joblog:<jobID> * line <text>` with `MAXLEN ~ 100000`, `Tail` as a blocking `XRANGE`/`XREAD` loop from the given id, and `Seal` reading the whole stream, writing `logs/<jobID>.txt` through `blobstore.Put`, then `DEL`-ing the Redis key.
- [x] Run `TEST_REDIS_URL=... TEST_S3_ENDPOINT=... go test ./internal/ci/ ./internal/blobstore/` — expect PASS.
- [x] Commit as `feat: stream live logs through redis and seal them into object storage`.

## Evidence

- Task 8: live log tails through Redis, sealed into S3-compatible object storage when the job ends, on top of the blobstore client.
- Built by a parallel agent in an isolated worktree under red-green TDD, merged to main, and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge): ok ci 7.880s, ok runner 1.059s, ok retention 3.456s, 0 failures. Real PostgreSQL 16, Redis 7 and MinIO in the kw cluster.
- A real flakiness source was found and fixed, not ignored: a blanket TRUNCATE per test raced against other suites sharing the external Postgres. Tests were rescoped to their own org, run and random bucket, with TRUNCATE reserved for the two that genuinely need exclusive access to the job queue.
- A real SECURITY defect was caught by the commit gate and fixed rather than suppressed: the runner executed the repository's workflow command directly on the runner host. A CI job runs whatever the workflow file says, so that command is attacker-controlled by construction. Jobs now run in their own Kubernetes pod built from the job's image, with the clone performed inside the pod; the host path requires NOVAFORGE_ALLOW_LOCAL_EXEC=1 and a runner outside a cluster without it refuses to start. TestLocalExecutionRefusedByDefault and TestPodExecutorCreatesIsolatedPod pin both halves.
- `go build ./...` and `go vet ./...` exit 0.
- Implementing commits: 87bfa97, 92f9da6, 36b23ed, 13d2b92, 4884078, 6c0bc8b, 58a0df0, b98555b, plus the isolation fix in d783e12.
