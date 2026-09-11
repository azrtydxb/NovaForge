# work-ci — implementation plan

Status: complete
Spec: .procoder/specs/backend-platform.md

## Goal

Deliver Work Items, Engineering Runs, and the CI system: typed engineering intent that can be
assigned to a human or an agent, pull requests that carry plan and proof rather than only a
diff, and runners that receive pushed jobs, stream logs, and upload artifacts.

## Architecture

Two new Go services — `work-reviews` and `ci-runner` — each with its own PostgreSQL schema and
gRPC API, both consuming the `stream:git:push` Redis stream produced by the foundation plan.
Runners hold a persistent outbound gRPC stream and the platform pushes jobs down it, so a
runner never needs inbound reachability; live log lines flow through Redis and are sealed into
MinIO when a job ends.

## Constraints

Taken verbatim from the spec; every task inherits these.

- Go 1.26 or later, PostgreSQL 16 or later with pgvector, Redis 7 or later with Streams and
  consumer groups, Kubernetes 1.29 or later.
- Internal transport is gRPC; the client edge is REST described by OpenAPI; events are Redis
  Streams.
- One PostgreSQL cluster, one schema per service. No service reads another service's tables;
  cross-service reads go through that service's gRPC API.
- Organizations are a hard security boundary: per-org namespaces, per-org credentials, and no
  data path between orgs. Every authorization check is org-scoped, and no query may be
  satisfiable without an org predicate.
- Redis durability is AOF persistence with consumer-group redelivery, giving at-least-once
  delivery. Every stream handler must be idempotent; exactly-once is not assumed anywhere.
- Air-gapped operation is a first-class deployment model. No feature may hard-depend on
  reaching a hosted model provider or the public internet.
- Kubernetes and Helm are the only supported deployment path; docker-compose is out of scope.
- Runners open a persistent outbound gRPC stream and the platform pushes jobs down it. Runners
  never need inbound network reachability.
- CI artifacts and sealed logs live in self-hosted S3-compatible object storage; live log tails
  stream through Redis.
- Retention is configurable per organization, defaulting to indefinite for provenance and gate
  outcomes and 90 days for raw CI and agent logs.
- Module path is `github.com/novaforge/novaforge`. Service binaries live under `cmd/<service>`,
  shared packages under `internal/<domain>`.

## Task 1: Work Item schema and store

Files: `internal/work/migrations/000001_work.up.sql`,
`internal/work/migrations/000001_work.down.sql`, `internal/work/store.go`,
`internal/work/store_test.go`

Interfaces: produces `work.Item` with fields
`ID uuid.UUID, OrgID uuid.UUID, RepoID uuid.UUID, Key string, Type string, Goal string, Acceptance []string, Constraints []string, RequiredGates []string, AssigneeID uuid.UUID, AssigneeKind string, State string, CreatedAt time.Time`
and `work.Store` with `Create(ctx, Item) (Item, error)`, `Get(ctx, id uuid.UUID) (Item, error)`,
`GetByKey(ctx, orgID uuid.UUID, key string) (Item, error)`,
`List(ctx, orgID, repoID uuid.UUID, state string) ([]Item, error)`, and
`Assign(ctx, id, assigneeID uuid.UUID, kind string) error`.

- [ ] Write the failing test `internal/work/store_test.go`:
      `func TestCreateWorkItemAllocatesKey(t *testing.T)` creates two items in one org and
      asserts their keys are `NF-1` and `NF-2`;
      `func TestRejectUnknownType(t *testing.T)` asserts creating an item with type `"nonsense"`
      returns an error containing "invalid type";
      `func TestAssignToAgent(t *testing.T)` assigns with kind `"agent"` and asserts `Get`
      round-trips it;
      `func TestListIsOrgScoped(t *testing.T)` creates items in two orgs and asserts a list for
      org A never returns org B's items. Run `go test ./internal/work/` — expect FAIL with
      "undefined: work.Store".
- [ ] Write the up migration creating `work_items` (id uuid pk, org_id uuid not null, repo_id
      uuid not null, seq bigint not null, key text not null, type text not null check (type in
      ('feature','bug','refactor','security','tech_debt','research','architecture','upgrade','incident','documentation')),
      goal text not null, acceptance text[] not null default '{}', constraints text[] not null
      default '{}', required_gates text[] not null default '{}', assignee_id uuid,
      assignee_kind text check (assignee_kind in ('user','agent')), state text not null default
      'open' check (state in ('open','planning','in_progress','review','done','blocked')),
      created_at timestamptz not null default now(), unique (org_id, key), unique (org_id, seq))
      plus `CREATE INDEX ON work_items (org_id, repo_id, state);` and the matching down
      migration dropping the table.
- [ ] Implement `Create` allocating `seq` inside the insert with
      `COALESCE((SELECT MAX(seq) FROM work_items WHERE org_id=$1), 0) + 1` in a single
      statement so concurrent creates cannot collide, and deriving `key` as `'NF-' || seq`.
- [ ] Implement every read with an explicit `org_id = $1` predicate taken from
      `authz.FromContext`, never from an argument supplied by the caller's request body.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/work/` — expect PASS.
- [ ] Commit as `feat: add work item schema and store`.

## Task 2: Engineering Runs — pull requests with plan and proof

Files: `internal/reviews/migrations/000001_reviews.up.sql`,
`internal/reviews/migrations/000001_reviews.down.sql`, `internal/reviews/store.go`,
`internal/reviews/store_test.go`

Interfaces: produces `reviews.Run` with fields
`ID, OrgID, RepoID, WorkItemID uuid.UUID, Number int, Title string, SourceRef, TargetRef string, State string, AuthorID uuid.UUID, AuthorKind string, AgentName, ModelName string, CreatedAt time.Time`,
`reviews.PlanStep{RunID uuid.UUID, Ordinal int, Text, State string}`,
`reviews.ProofRecord{RunID uuid.UUID, Gate string, Status string, Detail string, RecordedAt time.Time}`,
and `reviews.Store` with `CreateRun`, `GetRun`, `ListRuns`, `AddPlanStep`, `SetPlanStepState`,
`RecordProof`, `ListProof`, `AddComment`, and `SubmitReview(ctx, runID, reviewerID uuid.UUID, reviewerKind, verdict string) error`.

- [ ] Write the failing test `internal/reviews/store_test.go`:
      `func TestCreateRunAllocatesNumber(t *testing.T)` asserts two runs in one repository get
      numbers 1 and 2;
      `func TestProofRecordsAccumulate(t *testing.T)` records proof for gates `tests` and
      `security` and asserts `ListProof` returns both with their statuses;
      `func TestAuthorCannotBeSoleApprover(t *testing.T)` creates a run authored by agent A,
      calls `SubmitReview` with reviewer A and verdict `approve`, and asserts it returns an
      error containing "author cannot approve";
      `func TestSecondReviewerApproves(t *testing.T)` asserts reviewer B approving the same run
      succeeds. Run `go test ./internal/reviews/` — expect FAIL with "undefined: reviews.Store".
- [ ] Write the up migration creating `runs` (id uuid pk, org_id uuid not null, repo_id uuid not
      null, work_item_id uuid, number int not null, title text not null, source_ref text not
      null, target_ref text not null, state text not null default 'open' check (state in
      ('open','merged','closed')), author_id uuid not null, author_kind text not null check
      (author_kind in ('user','agent')), agent_name text, model_name text, created_at
      timestamptz not null default now(), unique (repo_id, number)), plus `run_plan_steps`,
      `run_proof` (unique (run_id, gate)), `run_comments`, and `run_reviews` (unique (run_id,
      reviewer_id)); and the matching down migration.
- [ ] Implement `SubmitReview` rejecting self-approval with
      `fmt.Errorf("author cannot approve their own run %s", runID)` whenever
      `reviewer_id = author_id`, regardless of `author_kind` — this is the enforcement point
      for the spec's independent-review requirement.
- [ ] Implement `RecordProof` as an upsert on `(run_id, gate)` so an at-least-once redelivery of
      the same gate result does not duplicate rows.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/reviews/` — expect PASS.
- [ ] Commit as `feat: add engineering runs with plan steps, proof records, and review rules`.

## Task 3: Change impact computation

Files: `internal/reviews/impact.go`, `internal/reviews/impact_test.go`

Interfaces: produces
`reviews.ComputeImpact(ctx context.Context, git gitv1.GitServiceClient, orgID, repoID uuid.UUID, from, to string) (Impact, error)`
where `Impact` is `{FilesChanged int, Insertions int, Deletions int, Paths []string}`. The
edge renders this as the CHANGE IMPACT block of an Engineering Run.

- [ ] Write the failing test `internal/reviews/impact_test.go`:
      `func TestComputeImpactCountsFiles(t *testing.T)` stubs `GitServiceClient.GetDiff` to
      return a two-file unified diff with three added and one removed line, then asserts
      `Impact{FilesChanged: 2, Insertions: 3, Deletions: 1}` and that `Paths` lists both files
      in order; `func TestComputeImpactEmptyDiff(t *testing.T)` asserts an empty diff yields a
      zero-valued `Impact` and a nil error. Run `go test ./internal/reviews/` — expect FAIL with
      "undefined: reviews.ComputeImpact".
- [ ] Implement `ComputeImpact` parsing the unified diff line-wise: count a file for each line
      beginning `diff --git `, extract the path from the `b/` side, count insertions for lines
      beginning `+` excluding `+++`, and deletions for lines beginning `-` excluding `---`.
- [ ] Run `go test ./internal/reviews/` — expect PASS.
- [ ] Commit as `feat: compute change impact from unified diff`.

## Task 4: Workflow definition parsing

Files: `internal/ci/workflow.go`, `internal/ci/workflow_test.go`

Interfaces: produces `ci.Workflow{Name string, Jobs map[string]Job}` and
`ci.Job{Run string, Agent string, Needs []string, Image string, Env map[string]string}`, plus
`ci.ParseWorkflow(data []byte) (Workflow, error)` and
`ci.TopoSort(w Workflow) ([]string, error)`. The scheduler in Task 5 consumes both. Workflow
files live at `.novaforge/workflow.yaml` in the repository under test.

- [ ] Write the failing test `internal/ci/workflow_test.go`:
      `func TestParseWorkflowWithAgentJob(t *testing.T)` parses the literal document
      `jobs:\n  test:\n    run: go test ./...\n  security-review:\n    agent: security\n` and
      asserts `Jobs["test"].Run == "go test ./..."` and `Jobs["security-review"].Agent == "security"`;
      `func TestRejectJobWithBothRunAndAgent(t *testing.T)` asserts a job declaring both returns
      an error containing "exactly one of";
      `func TestTopoSortRespectsNeeds(t *testing.T)` asserts a workflow where `b` needs `a`
      sorts `a` before `b`;
      `func TestTopoSortDetectsCycle(t *testing.T)` asserts a workflow where `a` needs `b` and
      `b` needs `a` returns an error containing "cycle". Run `go test ./internal/ci/` — expect
      FAIL with "undefined: ci.ParseWorkflow".
- [ ] Add `go get gopkg.in/yaml.v3` and implement `ParseWorkflow` with
      `yaml.Unmarshal`, rejecting a job with neither or both of `run` and `agent` using
      `fmt.Errorf("job %q must declare exactly one of run or agent", name)`, and rejecting a
      `needs` entry naming an unknown job.
- [ ] Implement `TopoSort` as Kahn's algorithm over the `Needs` edges, returning
      `errors.New("workflow contains a dependency cycle")` when nodes remain unvisited, and
      sorting ready nodes by name so the order is deterministic across runs.
- [ ] Run `go test ./internal/ci/` — expect PASS.
- [ ] Commit as `feat: add ci workflow parsing with topological job ordering`.

## Task 5: CI schema, scheduler, and push consumer

Files: `internal/ci/migrations/000001_ci.up.sql`, `internal/ci/migrations/000001_ci.down.sql`,
`internal/ci/store.go`, `internal/ci/scheduler.go`, `internal/ci/scheduler_test.go`

Interfaces: produces `ci.Store` with `CreateRun`, `GetRun`, `ListRuns`, `CreateJob`,
`ClaimJob(ctx, runnerID uuid.UUID, labels []string) (Job, error)`,
`SetJobStatus(ctx, jobID uuid.UUID, status string) error`, and
`ci.Scheduler.Run(ctx context.Context) error` which consumes `events.StreamGitPush` in the
consumer group `ci-engine`.

- [ ] Write the up migration creating `workflow_runs` (id uuid pk, org_id uuid not null, repo_id
      uuid not null, commit_sha text not null, ref text not null, status text not null default
      'queued' check (status in ('queued','running','success','failure','cancelled')),
      created_at timestamptz not null default now(), unique (repo_id, commit_sha, ref)),
      `workflow_jobs` (id uuid pk, run_id uuid not null references workflow_runs(id) on delete
      cascade, name text not null, needs text[] not null default '{}', run_cmd text, agent_role
      text, image text, status text not null default 'pending', runner_id uuid, started_at
      timestamptz, finished_at timestamptz, unique (run_id, name)), and `runners` (id uuid pk,
      org_id uuid not null, name text not null, labels text[] not null default '{}',
      token_hash bytea not null unique, last_seen_at timestamptz); plus the down migration.
- [ ] Write the failing test `internal/ci/scheduler_test.go`:
      `func TestPushSchedulesRun(t *testing.T)` publishes a `PushEvent` to the stream with a
      stubbed git client returning a workflow with one job, runs the scheduler for one
      iteration, and asserts a `workflow_runs` row exists with that commit SHA;
      `func TestDuplicatePushIsIdempotent(t *testing.T)` publishes the identical event twice and
      asserts exactly one run row exists — proving the handler tolerates at-least-once
      redelivery;
      `func TestJobBlockedUntilNeedsSucceed(t *testing.T)` asserts `ClaimJob` never returns a
      job whose `needs` are not all `success`;
      `func TestMissingWorkflowSkipsSilently(t *testing.T)` asserts a push to a repository with
      no `.novaforge/workflow.yaml` creates no run and returns no error. Run
      `go test ./internal/ci/` — expect FAIL with "undefined: ci.Scheduler".
- [ ] Implement the scheduler loop: `events.EnsureGroup`, then `XREADGROUP` with `Block` of 5s
      and `Count` of 10, `XACK` only after the handler returns nil, and an `XAUTOCLAIM` pass
      every 30s reclaiming messages idle longer than 60s.
- [ ] Implement idempotency by making run creation an
      `INSERT ... ON CONFLICT (repo_id, commit_sha, ref) DO NOTHING RETURNING id` and treating a
      zero-row result as "already scheduled, nothing to do".
- [ ] Implement `ClaimJob` as `SELECT ... FOR UPDATE SKIP LOCKED` over pending jobs whose
      `needs` are all satisfied, so two runners never claim the same job.
- [ ] Run `TEST_DATABASE_URL=... TEST_REDIS_URL=... go test ./internal/ci/` — expect PASS.
- [ ] Commit as `feat: add ci scheduler consuming push events idempotently`.

## Task 6: Runner protocol over a pushed gRPC stream

Files: `proto/ci/v1/ci.proto`, `internal/ci/grpc.go`, `internal/ci/dispatch.go`,
`internal/ci/dispatch_test.go`

Interfaces: produces `novaforge.ci.v1.RunnerService` with
`rpc Register(RegisterRequest) returns (RegisterResponse)`,
`rpc Connect(stream RunnerMessage) returns (stream JobMessage)`, and
`rpc ReportStatus(StatusRequest) returns (StatusResponse)`; plus
`ci.Dispatcher` with `Register(runnerID uuid.UUID, ch chan<- *civ1.JobMessage)`,
`Unregister(runnerID uuid.UUID)`, and
`Dispatch(ctx context.Context, job Job, labels []string) error`. The runner binary in Task 7
is the only client.

- [ ] Write the failing test `internal/ci/dispatch_test.go`:
      `func TestDispatchReachesMatchingRunner(t *testing.T)` registers two runners with labels
      `{"linux"}` and `{"windows"}`, dispatches a job requiring `linux`, and asserts only the
      first channel receives it;
      `func TestDispatchNoMatchingRunner(t *testing.T)` asserts dispatching a job whose labels
      match no runner returns an error containing "no runner";
      `func TestBrokenStreamOrphansJob(t *testing.T)` registers a runner, dispatches a job,
      unregisters the runner without a status report, and asserts the reaper marks the job
      `failure` with detail containing "runner disconnected". Run `go test ./internal/ci/` —
      expect FAIL with "undefined: ci.Dispatcher".
- [ ] Write `proto/ci/v1/ci.proto` with the three RPCs above, where `JobMessage` carries
      `job_id`, `run_id`, `repo_clone_url`, `commit_sha`, `run_cmd`, `agent_role`, `image`, and
      a map of `env`, and `RunnerMessage` carries `heartbeat` and `log_chunk` variants in a
      `oneof`. Run `make generate` and commit the generated code.
- [ ] Implement `Dispatcher` as a mutex-guarded map from runner id to channel plus its label
      set, selecting the least-recently-dispatched runner whose labels are a superset of the
      job's.
- [ ] Implement the reaper: on `Unregister`, any job still `running` for that runner is set to
      `failure` with detail `runner disconnected`, so a dead runner never leaves a job running
      indefinitely.
- [ ] Run `go test ./internal/ci/` — expect PASS.
- [ ] Commit as `feat: add runner dispatch over pushed grpc streams`.

## Task 7: Runner binary and job execution

Files: `cmd/runner/main.go`, `internal/runner/executor.go`, `internal/runner/executor_test.go`,
`Dockerfile.runner`

Interfaces: produces
`runner.Execute(ctx context.Context, job *civ1.JobMessage, workdir string, logs chan<- string) (exitCode int, err error)`.
It clones the repository at `job.CommitSha`, runs `job.RunCmd` inside a container built from
`job.Image`, and streams every output line into `logs`.

- [ ] Write the failing test `internal/runner/executor_test.go`:
      `func TestExecuteStreamsLogLines(t *testing.T)` runs a job whose command is
      `printf 'one\\ntwo\\n'` and asserts the `logs` channel yields exactly `"one"` then
      `"two"`;
      `func TestExecuteReturnsNonZeroExit(t *testing.T)` runs `exit 7` and asserts
      `exitCode == 7` with a nil error, since a failing job is a result and not a transport
      error;
      `func TestExecuteRespectsContextCancel(t *testing.T)` runs `sleep 60`, cancels the context
      after 100ms, and asserts the call returns within 5s. Run `go test ./internal/runner/` —
      expect FAIL with "undefined: runner.Execute".
- [ ] Implement `Execute` with `exec.CommandContext`, wiring stdout and stderr through an
      `io.Pipe` and a `bufio.Scanner` with a 1 MiB buffer so long lines do not truncate, and
      setting `Cancel` plus `WaitDelay` of 5s so a cancelled job is killed rather than leaked.
- [ ] Implement `cmd/runner/main.go`: register, open the `Connect` stream, send a heartbeat
      every 15s, execute each received job, forward log chunks as `RunnerMessage.log_chunk`,
      and call `ReportStatus` on completion. On stream error, reconnect with exponential
      backoff starting at 1s and capped at 60s.
- [ ] Write `Dockerfile.runner` on `golang:1.26` builder and `alpine:3.21` runtime with
      `RUN apk add --no-cache git ca-certificates`, since the runner clones repositories.
- [ ] Run `go test ./internal/runner/` — expect PASS.
- [ ] Commit as `feat: add runner binary with streaming job execution`.

## Task 8: Live logs through Redis and sealed logs in object storage

Files: `internal/ci/logs.go`, `internal/ci/logs_test.go`, `internal/blobstore/s3.go`,
`internal/blobstore/s3_test.go`

Interfaces: produces `blobstore.Client` with
`Put(ctx, key string, r io.Reader, size int64, contentType string) error`,
`Get(ctx, key string) (io.ReadCloser, error)`, and `Delete(ctx, key string) error`; plus
`ci.LogSink.Append(ctx, jobID uuid.UUID, line string) error`,
`ci.LogSink.Tail(ctx, jobID uuid.UUID, from string) (<-chan string, error)`, and
`ci.LogSink.Seal(ctx, jobID uuid.UUID) (objectKey string, err error)`.

- [ ] Write the failing test `internal/blobstore/s3_test.go`:
      `func TestPutGetRoundTrip(t *testing.T)` skips unless `TEST_S3_ENDPOINT` is set, writes
      `"hello"` and asserts `Get` returns it;
      `func TestGetMissingKey(t *testing.T)` asserts a missing key returns an error satisfying
      `errors.Is(err, blobstore.ErrNotFound)`. Run `go test ./internal/blobstore/` — expect FAIL
      with "undefined: blobstore.Client".
- [ ] Add `go get github.com/minio/minio-go/v7` and implement `Client` against it, configured
      by `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, and `S3_BUCKET`, creating the bucket
      when absent so an air-gapped first boot needs no manual setup.
- [ ] Write the failing test `internal/ci/logs_test.go`:
      `func TestTailReceivesAppendedLines(t *testing.T)` starts a tail, appends three lines, and
      asserts all three arrive in order;
      `func TestSealMovesLogToObjectStorage(t *testing.T)` appends two lines, seals, asserts the
      returned key is fetchable from the blobstore with both lines, and asserts the Redis stream
      key no longer exists. Run `go test ./internal/ci/` — expect FAIL with "undefined:
      ci.LogSink".
- [ ] Implement `Append` as `XADD joblog:<jobID> * line <text>` with `MAXLEN ~ 100000`, `Tail`
      as a blocking `XRANGE`/`XREAD` loop from the given id, and `Seal` reading the whole stream,
      writing `logs/<jobID>.txt` through `blobstore.Put`, then `DEL`-ing the Redis key.
- [ ] Run `TEST_REDIS_URL=... TEST_S3_ENDPOINT=... go test ./internal/ci/ ./internal/blobstore/`
      — expect PASS.
- [ ] Commit as `feat: stream live logs through redis and seal them into object storage`.

## Task 9: Artifacts

Files: `internal/ci/artifacts.go`, `internal/ci/artifacts_test.go`,
`internal/ci/migrations/000002_artifacts.up.sql`,
`internal/ci/migrations/000002_artifacts.down.sql`

Interfaces: produces
`ci.ArtifactStore.Upload(ctx, jobID uuid.UUID, name string, r io.Reader, size int64) (Artifact, error)`,
`ci.ArtifactStore.List(ctx, runID uuid.UUID) ([]Artifact, error)`, and
`ci.ArtifactStore.Open(ctx, id uuid.UUID) (io.ReadCloser, error)`, where `Artifact` is
`{ID, JobID uuid.UUID, Name string, SizeBytes int64, ObjectKey string, CreatedAt time.Time}`.

- [ ] Write the up migration creating `artifacts` (id uuid pk, org_id uuid not null, job_id uuid
      not null references workflow_jobs(id) on delete cascade, name text not null, size_bytes
      bigint not null, object_key text not null, created_at timestamptz not null default now(),
      unique (job_id, name)) and the matching down migration.
- [ ] Write the failing test `internal/ci/artifacts_test.go`:
      `func TestUploadThenOpen(t *testing.T)` uploads 12 bytes named `report.txt` and asserts
      `Open` returns the same bytes;
      `func TestListIsScopedToRun(t *testing.T)` uploads artifacts against two jobs of different
      runs and asserts `List` returns only the requested run's;
      `func TestDuplicateArtifactNameRejected(t *testing.T)` asserts a second upload with the
      same name for the same job returns an error containing "already exists". Run
      `go test ./internal/ci/` — expect FAIL with "undefined: ci.ArtifactStore".
- [ ] Implement `Upload` writing to `artifacts/<orgID>/<jobID>/<name>` through `blobstore.Put`
      and recording the row, deleting the object again if the insert fails so storage never
      holds an unreferenced blob.
- [ ] Run `TEST_DATABASE_URL=... TEST_S3_ENDPOINT=... go test ./internal/ci/` — expect PASS.
- [ ] Commit as `feat: add ci artifact upload and retrieval`.

## Task 10: Retention policy

Files: `internal/retention/policy.go`, `internal/retention/policy_test.go`,
`internal/retention/migrations/000001_retention.up.sql`,
`internal/retention/migrations/000001_retention.down.sql`

Interfaces: produces `retention.Policy{OrgID uuid.UUID, LogDays int, EvidenceDays int}` with
`retention.Store.Get(ctx, orgID uuid.UUID) (Policy, error)` returning the default
`{LogDays: 90, EvidenceDays: 0}` when unset, where `0` means keep indefinitely, and
`retention.Sweeper.Sweep(ctx context.Context) (deleted int, err error)`.

- [ ] Write the up migration creating `retention_policies` (org_id uuid primary key, log_days int
      not null default 90 check (log_days >= 0), evidence_days int not null default 0 check
      (evidence_days >= 0)) and the matching down migration.
- [ ] Write the failing test `internal/retention/policy_test.go`:
      `func TestDefaultPolicyWhenUnset(t *testing.T)` asserts an org with no row yields
      `LogDays == 90` and `EvidenceDays == 0`;
      `func TestSweepDeletesExpiredLogsOnly(t *testing.T)` seeds one sealed log 100 days old and
      one 10 days old plus a 100-day-old proof record, runs `Sweep`, and asserts the old log
      object is gone while both the recent log and the proof record remain;
      `func TestSweepHonoursZeroAsForever(t *testing.T)` asserts `EvidenceDays == 0` deletes no
      evidence at any age. Run `go test ./internal/retention/` — expect FAIL with "undefined:
      retention.Sweeper".
- [ ] Implement `Sweep` iterating orgs, deleting sealed log objects and artifact objects older
      than `LogDays` through `blobstore.Delete` before deleting their rows, and skipping any
      category whose configured days is zero.
- [ ] Run `TEST_DATABASE_URL=... TEST_S3_ENDPOINT=... go test ./internal/retention/` — expect
      PASS.
- [ ] Commit as `feat: add per-org retention policy with sweeper`.

## Task 11: work-reviews and ci-runner services and their REST routes

Files: `proto/work/v1/work.proto`, `proto/reviews/v1/reviews.proto`,
`cmd/work-reviews/main.go`, `cmd/ci-runner/main.go`, `internal/edge/work_routes.go`,
`internal/edge/ci_routes.go`, `internal/edge/work_routes_test.go`, `api/openapi.yaml`,
`Dockerfile.work-reviews`, `Dockerfile.ci-runner`

Interfaces: produces `novaforge.work.v1.WorkService` (`CreateItem`, `GetItem`, `ListItems`,
`AssignItem`), `novaforge.reviews.v1.ReviewsService` (`CreateRun`, `GetRun`, `ListRuns`,
`AddPlanStep`, `RecordProof`, `SubmitReview`, `AddComment`), and
`novaforge.ci.v1.CIService` (`ListRuns`, `GetRun`, `GetJobLogs`, `ListArtifacts`,
`DispatchWorkflow`). The edge mounts these under the repository path established by the
foundation plan.

- [ ] Extend `api/openapi.yaml` with `GET|POST /api/v1/orgs/{org}/repos/{repo}/work`,
      `GET|PATCH /api/v1/orgs/{org}/repos/{repo}/work/{key}`,
      `GET|POST /api/v1/orgs/{org}/repos/{repo}/runs`,
      `GET /api/v1/orgs/{org}/repos/{repo}/runs/{number}`,
      `GET /api/v1/orgs/{org}/repos/{repo}/runs/{number}/proof`,
      `POST /api/v1/orgs/{org}/repos/{repo}/runs/{number}/reviews`,
      `GET /api/v1/orgs/{org}/repos/{repo}/ci/runs`,
      `GET /api/v1/orgs/{org}/repos/{repo}/ci/runs/{id}`,
      `GET /api/v1/orgs/{org}/repos/{repo}/ci/jobs/{id}/logs`, and
      `GET /api/v1/orgs/{org}/repos/{repo}/ci/jobs/{id}/artifacts`.
- [ ] Write the failing test `internal/edge/work_routes_test.go`:
      `func TestCreateWorkItemRoute(t *testing.T)` posts a Work Item and asserts 201 with the
      allocated key in the body;
      `func TestJobLogsStreamAsSSE(t *testing.T)` asserts the logs route responds with
      `Content-Type: text/event-stream` and delivers appended lines as `data:` frames;
      `func TestCrossOrgRunDenied(t *testing.T)` asserts a session for org A requesting org B's
      run returns 403. Run `go test ./internal/edge/` — expect FAIL with "undefined:
      edge.mountWorkRoutes".
- [ ] Implement the two service binaries following the foundation plan's shape: migrate their
      own schema (`work`, `reviews`, and `ci`), serve gRPC on 9093 and 9094, expose `/healthz`,
      and drain for 30s on SIGTERM. `cmd/ci-runner/main.go` additionally starts the scheduler
      loop and the retention sweeper on a 1h ticker.
- [ ] Implement the edge routes, rendering live job logs as server-sent events and finished job
      logs as a plain body read from object storage.
- [ ] Run `go test ./internal/edge/` and `make test` — expect PASS. Re-run
      `TestEveryRouteIsInOpenAPI` from the foundation plan — expect PASS, proving the new routes
      are documented.
- [ ] Commit as `feat: add work-reviews and ci-runner services with rest routes`.

## Task 12: Helm additions and the end-to-end work-to-artifact test

Files: `deploy/helm/novaforge/templates/work-reviews.yaml`,
`deploy/helm/novaforge/templates/ci-runner.yaml`,
`deploy/helm/novaforge/templates/runner.yaml`, `deploy/helm/novaforge/values.yaml`,
`tests/e2e/work_ci_test.sh`

Interfaces: produces Services `novaforge-work-reviews:9093` and `novaforge-ci-runner:9094`, and
a `runner` Deployment with a configurable replica count and label set from
`values.yaml` key `runner.labels`.

- [ ] Write the failing test `tests/e2e/work_ci_test.sh`: on the kind cluster from the
      foundation plan, `helm upgrade novaforge ./deploy/helm/novaforge --wait --timeout 10m`,
      then push a repository containing `.novaforge/workflow.yaml` with a single job running
      `echo hello && echo hi > out.txt`, poll `nf ci runs` until the run reports `success`
      within 300s, assert the job log contains `hello`, and assert the artifact `out.txt` is
      downloadable. Run `bash tests/e2e/work_ci_test.sh` — expect FAIL with
      "Error: no matching deployment novaforge-ci-runner".
- [ ] Write the three templates, giving the runner Deployment no inbound Service at all, since
      runners dial out and never accept connections.
- [ ] Extend `values.yaml` with `runner.replicas` defaulting to `2`, `runner.labels` defaulting
      to `["linux"]`, and MinIO credentials wired into the ci-runner Deployment.
- [ ] Run `bash tests/e2e/work_ci_test.sh` — expect PASS.
- [ ] Commit as `feat: deploy work-reviews, ci-runner, and runners via helm`.
