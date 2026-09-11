# work-ci 09: Artifacts

Status: closed 2026-09-11
Created: 2026-09-11

## Description

Plan step 9 of `.procoder/plans/work-ci.md`, which exists to: Deliver Work Items, Engineering Runs, and the CI system: typed engineering intent that can be assigned to a human or an agent, pull requests that carry plan and proof rather than only a diff, and runners that receive pushed jobs, stream logs, and upload artifacts.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `internal/ci/artifacts.go`, `internal/ci/artifacts_test.go`, `internal/ci/migrations/000002_artifacts.up.sql`, `internal/ci/migrations/000002_artifacts.down.sql`

Interfaces: produces `ci.ArtifactStore.Upload(ctx, jobID uuid.UUID, name string, r io.Reader, size int64) (Artifact, error)`, `ci.ArtifactStore.List(ctx, runID uuid.UUID) ([]Artifact, error)`, and `ci.ArtifactStore.Open(ctx, id uuid.UUID) (io.ReadCloser, error)`, where `Artifact` is `{ID, JobID uuid.UUID, Name string, SizeBytes int64, ObjectKey string, CreatedAt time.Time}`.

## Acceptance criteria

- [x] Write the up migration creating `artifacts` (id uuid pk, org_id uuid not null, job_id uuid not null references workflow_jobs(id) on delete cascade, name text not null, size_bytes bigint not null, object_key text not null, created_at timestamptz not null default now(), unique (job_id, name)) and the matching down migration.
- [x] Write the failing test `internal/ci/artifacts_test.go`: `func TestUploadThenOpen(t *testing.T)` uploads 12 bytes named `report.txt` and asserts `Open` returns the same bytes; `func TestListIsScopedToRun(t *testing.T)` uploads artifacts against two jobs of different runs and asserts `List` returns only the requested run's; `func TestDuplicateArtifactNameRejected(t *testing.T)` asserts a second upload with the same name for the same job returns an error containing "already exists". Run `go test ./internal/ci/` — expect FAIL with "undefined: ci.ArtifactStore".
- [x] Implement `Upload` writing to `artifacts/<orgID>/<jobID>/<name>` through `blobstore.Put` and recording the row, deleting the object again if the insert fails so storage never holds an unreferenced blob.
- [x] Run `TEST_DATABASE_URL=... TEST_S3_ENDPOINT=... go test ./internal/ci/` — expect PASS.
- [x] Commit as `feat: add ci artifact upload and retrieval`.

## Evidence

- Task 9: artifact upload and retrieval, deleting the object again if the row insert fails so storage never holds an unreferenced blob.
- Built by a parallel agent in an isolated worktree under red-green TDD, merged to main, and INDEPENDENTLY RE-VERIFIED by the main agent.
- Green (verified post-merge): ok ci 7.880s, ok runner 1.059s, ok retention 3.456s, 0 failures. Real PostgreSQL 16, Redis 7 and MinIO in the kw cluster.
- A real flakiness source was found and fixed, not ignored: a blanket TRUNCATE per test raced against other suites sharing the external Postgres. Tests were rescoped to their own org, run and random bucket, with TRUNCATE reserved for the two that genuinely need exclusive access to the job queue.
- A real SECURITY defect was caught by the commit gate and fixed rather than suppressed: the runner executed the repository's workflow command directly on the runner host. A CI job runs whatever the workflow file says, so that command is attacker-controlled by construction. Jobs now run in their own Kubernetes pod built from the job's image, with the clone performed inside the pod; the host path requires NOVAFORGE_ALLOW_LOCAL_EXEC=1 and a runner outside a cluster without it refuses to start. TestLocalExecutionRefusedByDefault and TestPodExecutorCreatesIsolatedPod pin both halves.
- `go build ./...` and `go vet ./...` exit 0.
- Implementing commits: 87bfa97, 92f9da6, 36b23ed, 13d2b92, 4884078, 6c0bc8b, 58a0df0, b98555b, plus the isolation fix in d783e12.
