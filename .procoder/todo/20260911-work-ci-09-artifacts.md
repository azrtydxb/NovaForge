# work-ci 09: Artifacts

Status: open
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

- [ ] Write the up migration creating `artifacts` (id uuid pk, org_id uuid not null, job_id uuid not null references workflow_jobs(id) on delete cascade, name text not null, size_bytes bigint not null, object_key text not null, created_at timestamptz not null default now(), unique (job_id, name)) and the matching down migration.
- [ ] Write the failing test `internal/ci/artifacts_test.go`: `func TestUploadThenOpen(t *testing.T)` uploads 12 bytes named `report.txt` and asserts `Open` returns the same bytes; `func TestListIsScopedToRun(t *testing.T)` uploads artifacts against two jobs of different runs and asserts `List` returns only the requested run's; `func TestDuplicateArtifactNameRejected(t *testing.T)` asserts a second upload with the same name for the same job returns an error containing "already exists". Run `go test ./internal/ci/` — expect FAIL with "undefined: ci.ArtifactStore".
- [ ] Implement `Upload` writing to `artifacts/<orgID>/<jobID>/<name>` through `blobstore.Put` and recording the row, deleting the object again if the insert fails so storage never holds an unreferenced blob.
- [ ] Run `TEST_DATABASE_URL=... TEST_S3_ENDPOINT=... go test ./internal/ci/` — expect PASS.
- [ ] Commit as `feat: add ci artifact upload and retrieval`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
