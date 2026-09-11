# work-ci 08: Live logs through Redis and sealed logs in object storage

Status: open
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

- [ ] Write the failing test `internal/blobstore/s3_test.go`: `func TestPutGetRoundTrip(t *testing.T)` skips unless `TEST_S3_ENDPOINT` is set, writes `"hello"` and asserts `Get` returns it; `func TestGetMissingKey(t *testing.T)` asserts a missing key returns an error satisfying `errors.Is(err, blobstore.ErrNotFound)`. Run `go test ./internal/blobstore/` — expect FAIL with "undefined: blobstore.Client".
- [ ] Add `go get github.com/minio/minio-go/v7` and implement `Client` against it, configured by `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, and `S3_BUCKET`, creating the bucket when absent so an air-gapped first boot needs no manual setup.
- [ ] Write the failing test `internal/ci/logs_test.go`: `func TestTailReceivesAppendedLines(t *testing.T)` starts a tail, appends three lines, and asserts all three arrive in order; `func TestSealMovesLogToObjectStorage(t *testing.T)` appends two lines, seals, asserts the returned key is fetchable from the blobstore with both lines, and asserts the Redis stream key no longer exists. Run `go test ./internal/ci/` — expect FAIL with "undefined: ci.LogSink".
- [ ] Implement `Append` as `XADD joblog:<jobID> * line <text>` with `MAXLEN ~ 100000`, `Tail` as a blocking `XRANGE`/`XREAD` loop from the given id, and `Seal` reading the whole stream, writing `logs/<jobID>.txt` through `blobstore.Put`, then `DEL`-ing the Redis key.
- [ ] Run `TEST_REDIS_URL=... TEST_S3_ENDPOINT=... go test ./internal/ci/ ./internal/blobstore/` — expect PASS.
- [ ] Commit as `feat: stream live logs through redis and seal them into object storage`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
