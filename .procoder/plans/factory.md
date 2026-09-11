# factory — implementation plan

Status: complete
Spec: .procoder/specs/backend-platform.md

## Goal

Close the loop from AI coding assistant to AI software engineering organization: decompose an
epic into dependency-ordered subtasks across specialized agents, review changes with independent
agents rather than their author, detect maintenance work autonomously and propose it as Work
Items, and auto-merge only where policy already allows it.

## Architecture

No new service: the swarm planner and the maintenance scanner are components of the existing
`work-reviews` and `agent-runtime` services, because both operate on Work Items and Agent Runs
that already live there. The planner writes subtasks as ordinary Work Items with dependency
edges, so everything downstream — gates, runs, merges — applies unchanged.

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
- An agent must not be both author and sole reviewer of a change. Independent reviewer, security,
  test, and architecture agents review it, ideally on different models to avoid correlated
  failure.
- Agents cannot bypass gates — enforcement is structural, not convention. Auto-merge is
  policy-controlled and still routes through the gate controller.
- The model never decides its own permissions; approval requirements come from policy.
- Agent runs are bounded by wall-clock, token, and cost limits, and exceeding any limit
  terminates the run while retaining its evidence.
- Autonomous maintenance proposes Work Items for approval; it never executes a fix unapproved.
- Air-gapped operation is a first-class deployment model. No feature may hard-depend on
  reaching a hosted model provider or the public internet.
- No provider-specific AI logic in NovaForge — model access is exclusively through go-ai-sdk.
- Redis durability is AOF persistence with consumer-group redelivery, giving at-least-once
  delivery. Every stream handler must be idempotent.
- Kubernetes and Helm are the only supported deployment path; docker-compose is out of scope.
- Module path is `github.com/novaforge/novaforge`. Service binaries live under `cmd/<service>`,
  shared packages under `internal/<domain>`.

## Task 1: Work Item dependency edges

Files: `internal/work/migrations/000002_deps.up.sql`,
`internal/work/migrations/000002_deps.down.sql`, `internal/work/deps.go`,
`internal/work/deps_test.go`

Interfaces: produces `work.Store.AddDependency(ctx, blockedID, blockerID uuid.UUID) error`,
`work.Store.Dependencies(ctx, id uuid.UUID) ([]Item, error)`,
`work.Store.Dependents(ctx, id uuid.UUID) ([]Item, error)`, and
`work.Store.Ready(ctx, orgID, epicID uuid.UUID) ([]Item, error)` returning only children whose
every blocker is in state `done`.

- [ ] Write the up migration creating `work_item_deps` (blocked_id uuid not null references
      work_items(id) on delete cascade, blocker_id uuid not null references work_items(id) on
      delete cascade, primary key (blocked_id, blocker_id), check (blocked_id <> blocker_id)) and
      adding `parent_id uuid references work_items(id)` to `work_items`, plus the matching down
      migration.
- [ ] Write the failing test `internal/work/deps_test.go`:
      `func TestReadyExcludesBlockedItems(t *testing.T)` creates children `A` and `B` where `B`
      depends on `A` and asserts `Ready` returns only `A`;
      `func TestReadyIncludesUnblockedAfterDone(t *testing.T)` marks `A` done and asserts `Ready`
      then returns `B`;
      `func TestFailedBlockerKeepsDependentsBlocked(t *testing.T)` marks `A` as `blocked` and
      asserts `B` is still absent from `Ready`;
      `func TestCycleRejected(t *testing.T)` asserts adding a dependency that closes a cycle
      returns an error containing "cycle";
      `func TestSelfDependencyRejected(t *testing.T)` asserts an item cannot depend on itself.
      Run `go test ./internal/work/` — expect FAIL with "undefined: work.Store.AddDependency".
- [ ] Implement `AddDependency` performing a recursive `WITH RECURSIVE` reachability check before
      inserting, returning `fmt.Errorf("dependency would create a cycle: %s -> %s", blocked, blocker)`.
- [ ] Implement `Ready` as a single query requiring
      `NOT EXISTS (SELECT 1 FROM work_item_deps d JOIN work_items b ON b.id = d.blocker_id WHERE d.blocked_id = w.id AND b.state <> 'done')`.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/work/` — expect PASS.
- [ ] Commit as `feat: add work item dependency edges with cycle rejection`.

## Task 2: Epic decomposition

Files: `internal/swarm/planner.go`, `internal/swarm/planner_test.go`

Interfaces: produces `swarm.Subtask{Title, Goal, Type, AgentRole string, DependsOn []string, Key string}`
and `swarm.Planner.Decompose(ctx context.Context, epic work.Item, bundle ctxasm.Bundle) ([]Subtask, error)`,
plus `swarm.Planner.Materialise(ctx context.Context, epic work.Item, subs []Subtask) ([]work.Item, error)`
which writes the subtasks as Work Items with `parent_id` set and dependency edges created.

- [ ] Write the failing test `internal/swarm/planner_test.go`:
      `func TestDecomposeProducesOrderedSubtasks(t *testing.T)` drives a stub model returning the
      six subtasks of the Enterprise SSO example — database changes, OAuth backend, admin
      configuration, frontend, documentation, integration tests — and asserts the OAuth backend
      depends on the database changes;
      `func TestMaterialiseCreatesChildWorkItems(t *testing.T)` asserts six Work Items exist with
      `parent_id` equal to the epic and that `Ready` initially returns only the dependency-free
      ones;
      `func TestUnknownAgentRoleRejected(t *testing.T)` asserts a subtask naming a role absent
      from the repository's agent configuration returns an error containing "unknown agent role";
      `func TestDecompositionCycleRejected(t *testing.T)` asserts a model returning mutually
      dependent subtasks is refused rather than materialised;
      `func TestMaterialiseIsIdempotent(t *testing.T)` calls `Materialise` twice with the same
      subtask keys and asserts six items exist, not twelve. Run `go test ./internal/swarm/` —
      expect FAIL with "undefined: swarm.Planner".
- [ ] Implement `Decompose` through go-ai-sdk structured output, requesting a strict schema of
      subtasks so the result is parsed rather than scraped from prose.
- [ ] Implement `Materialise` in one transaction, keyed on `(parent_id, key)` with
      `ON CONFLICT DO NOTHING`, and validating the whole dependency set against Task 1's cycle
      check before writing any row.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/swarm/` — expect PASS.
- [ ] Commit as `feat: decompose epics into dependency-ordered subtasks`.

## Task 3: Swarm scheduling across specialized agents

Files: `internal/swarm/scheduler.go`, `internal/swarm/scheduler_test.go`

Interfaces: produces `swarm.Scheduler.Tick(ctx context.Context, epicID uuid.UUID) (started int, err error)`
which starts an Agent Run for every ready subtask whose role maps to an enabled agent, and
`swarm.Scheduler.Run(ctx context.Context) error` ticking every open epic on a 30s interval.

- [ ] Write the failing test `internal/swarm/scheduler_test.go`:
      `func TestTickStartsOnlyReadySubtasks(t *testing.T)` asserts a tick over the Enterprise SSO
      epic starts runs solely for the dependency-free subtasks;
      `func TestBlockedDependentNotStarted(t *testing.T)` asserts the OAuth backend subtask gets
      no run while the database subtask is unfinished;
      `func TestFailedPrerequisiteBlocksDependents(t *testing.T)` fails the database subtask and
      asserts the dependent never starts and the epic reports `blocked`;
      `func TestTickIsIdempotent(t *testing.T)` ticks twice with no state change in between and
      asserts the second tick starts zero runs;
      `func TestConcurrencyCapRespected(t *testing.T)` asserts no more than
      `MaxConcurrentRuns` runs are active for one epic. Run `go test ./internal/swarm/` — expect
      FAIL with "undefined: swarm.Scheduler".
- [ ] Implement `Tick` claiming each subtask with
      `UPDATE work_items SET state='in_progress' WHERE id=$1 AND state='open' RETURNING id`, so
      two concurrent ticks cannot start the same subtask twice.
- [ ] Implement the concurrency cap from the repository configuration key
      `project.max_concurrent_runs`, defaulting to 5.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/swarm/` — expect PASS.
- [ ] Commit as `feat: schedule swarm subtasks with dependency ordering`.

## Task 4: Independent multi-agent review

Files: `internal/reviews/agentreview.go`, `internal/reviews/agentreview_test.go`

Interfaces: produces
`reviews.AgentReviewer.ReviewRun(ctx context.Context, runID uuid.UUID, roles []string) ([]AgentVerdict, error)`
where `AgentVerdict` is `{Role, AgentName, ModelName, Verdict, Summary string}` and `Verdict` is
one of `approve`, `request_changes`, or `comment`. The default roles are reviewer, security,
test, and architecture.

- [ ] Write the failing test `internal/reviews/agentreview_test.go`:
      `func TestAuthorAgentExcludedFromReviewers(t *testing.T)` asserts a run authored by the
      backend agent never dispatches a review to that same agent, and that the returned verdicts
      contain no entry naming it;
      `func TestReviewersUseDistinctModelsWhenAvailable(t *testing.T)` configures two models and
      asserts reviewer and security verdicts do not share a `ModelName`;
      `func TestRequestChangesBlocksMerge(t *testing.T)` asserts a `request_changes` verdict
      leaves `MayMerge` false;
      `func TestAllApprovalsStillRequireGates(t *testing.T)` asserts four approving verdicts with
      a failing tests gate still leave `MayMerge` false — review never substitutes for a gate;
      `func TestReviewVerdictsRecordedAsProof(t *testing.T)` asserts each verdict is written
      through `RecordProof` so it appears in the run's PROOF block. Run
      `go test ./internal/reviews/` — expect FAIL with "undefined: reviews.AgentReviewer".
- [ ] Implement `ReviewRun` filtering the role list against the run's author agent id before
      dispatch, so exclusion happens before any model call rather than being checked afterwards.
- [ ] Implement model diversity by round-robining the configured models across roles, falling
      back to a single model with a logged warning when only one is configured.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/reviews/` — expect PASS.
- [ ] Commit as `feat: add independent multi-agent review`.

## Task 5: Autonomous maintenance scanners

Files: `internal/maintenance/scan.go`, `internal/maintenance/deps.go`,
`internal/maintenance/flaky.go`, `internal/maintenance/deadcode.go`,
`internal/maintenance/coverage.go`, `internal/maintenance/docs.go`,
`internal/maintenance/perf.go`, `internal/maintenance/arch.go`,
`internal/maintenance/scan_test.go`, `internal/maintenance/flaky_test.go`

Interfaces: produces `maintenance.Finding{Kind, Title, Detail, Severity string, Paths []string, ProposedType string}`
with `Kind` one of `outdated_dependency`, `cve`, `flaky_test`, `dead_code`,
`coverage_regression`, `documentation_drift`, `performance_regression`, or
`architectural_violation`; plus
`type Scanner func(ctx context.Context, in ScanInput) ([]Finding, error)` and
`maintenance.Scanners` mapping each kind to its scanner.

- [ ] Write the failing test `internal/maintenance/scan_test.go`:
      `func TestScannersCoverAllEightKinds(t *testing.T)` asserts the keys of
      `maintenance.Scanners` are exactly the eight kinds above, sorted;
      `func TestCVEScannerProducesCriticalFinding(t *testing.T)` stubs the procoder security
      runner with a known advisory and asserts a finding of kind `cve` with severity `critical`;
      `func TestScannerFailureIsIsolated(t *testing.T)` asserts one scanner returning an error
      does not prevent the remaining seven from reporting. Run `go test ./internal/maintenance/`
      — expect FAIL with "undefined: maintenance.Scanners".
- [ ] Write the failing test `internal/maintenance/flaky_test.go`:
      `func TestFlakyDetectedFromMixedResults(t *testing.T)` seeds ten historical job results for
      one test name where the same commit SHA both passed and failed, and asserts a `flaky_test`
      finding naming it;
      `func TestConsistentFailureIsNotFlaky(t *testing.T)` asserts a test failing on every run
      produces no flaky finding, since that is a plain failure.
- [ ] Implement the dependency and CVE scanners by invoking the procoder deps and security
      commands, the coverage scanner by comparing the latest tests-gate coverage against the
      previous evaluation for the same repository, and the architectural scanner by reusing the
      architecture gate's import-graph check against the default branch.
- [ ] Implement the dead-code, documentation-drift, and performance-regression scanners against
      the graph service: symbols with no inbound `called_by` edge and no test coverage, context
      documents whose referenced symbols no longer exist, and benchmark artifacts whose latest
      value regressed more than the configured percentage.
- [ ] Run `go test ./internal/maintenance/` — expect PASS.
- [ ] Commit as `feat: add eight autonomous maintenance scanners`.

## Task 6: Proposing maintenance Work Items

Files: `internal/maintenance/propose.go`, `internal/maintenance/propose_test.go`

Interfaces: produces
`maintenance.Proposer.Propose(ctx context.Context, orgID, repoID uuid.UUID, findings []Finding) ([]work.Item, error)`
creating Work Items in state `open` with `assignee_kind` unset, and
`maintenance.Proposer.Run(ctx context.Context) error` scanning every repository on a 24h ticker.

- [ ] Write the failing test `internal/maintenance/propose_test.go`:
      `func TestProposalCreatesUnassignedWorkItem(t *testing.T)` asserts a CVE finding yields a
      Work Item of type `security` in state `open` with no assignee — nothing is executed
      unapproved;
      `func TestDuplicateFindingDoesNotDuplicateWorkItem(t *testing.T)` proposes the identical
      finding twice and asserts one Work Item exists;
      `func TestResolvedFindingClosesProposal(t *testing.T)` proposes a finding, then rescans
      without it, and asserts the Work Item is moved to `done` with a note rather than deleted;
      `func TestProposalNeverStartsAnAgentRun(t *testing.T)` asserts no Agent Run exists for any
      proposed item after `Propose` returns. Run `go test ./internal/maintenance/` — expect FAIL
      with "undefined: maintenance.Proposer".
- [ ] Implement deduplication with a stable fingerprint of `kind` plus sorted `Paths` plus
      `Title`, stored on the Work Item and enforced by a unique index.
- [ ] Run `TEST_DATABASE_URL=... go test ./internal/maintenance/` — expect PASS.
- [ ] Commit as `feat: propose maintenance work items without executing them`.

## Task 7: Policy-controlled auto-merge

Files: `internal/reviews/automerge.go`, `internal/reviews/automerge_test.go`

Interfaces: produces `reviews.AutoMergePolicy{Enabled bool, MaxFilesChanged int, AllowedTypes []string, ForbiddenPaths []string}`
and `reviews.AutoMerger.Consider(ctx context.Context, runID uuid.UUID) (merged bool, reason string, err error)`.
It calls the same `Merger.Merge` as a human would; it has no privileged path.

- [ ] Write the failing test `internal/reviews/automerge_test.go`:
      `func TestAutoMergeRefusedWhenGateFails(t *testing.T)` asserts a failing gate yields
      `merged == false` with a reason naming the gate;
      `func TestAutoMergeRefusedForForbiddenPath(t *testing.T)` asserts a change touching
      `internal/auth/` under a policy forbidding it is refused even with all gates green;
      `func TestAutoMergeRefusedAboveFileCap(t *testing.T)` asserts a 40-file change under a cap
      of 20 is refused;
      `func TestAutoMergeRefusedWhenDisabled(t *testing.T)` asserts a disabled policy never
      merges;
      `func TestAutoMergeUsesTheSameMergePath(t *testing.T)` asserts `Consider` reaches
      `Merger.Merge` and therefore `MayMerge`, so auto-merge cannot bypass the controller. Run
      `go test ./internal/reviews/` — expect FAIL with "undefined: reviews.AutoMerger".
- [ ] Implement `Consider` as a conjunction evaluated before delegating, with every refusal
      returning a human-readable reason recorded on the run.
- [ ] Run `go test ./internal/reviews/` — expect PASS.
- [ ] Commit as `feat: add policy-controlled auto-merge through the gate controller`.

## Task 8: Exception dashboard data

Files: `internal/reviews/exceptions.go`, `internal/reviews/exceptions_test.go`,
`internal/edge/factory_routes.go`, `api/openapi.yaml`

Interfaces: produces `reviews.Summary{AgentsRunning, ReadyToAutoMerge, NeedHumanReview, ArchitectureDecisions, GateFailures, AgentsBlocked int}`
and `reviews.Exceptions(ctx context.Context, orgID uuid.UUID) (Summary, []Item, error)`, where
each `Item` carries the Work Item key, title, state, and the reason it needs attention.

- [ ] Extend `api/openapi.yaml` with `GET /api/v1/orgs/{org}/dashboard`,
      `GET /api/v1/orgs/{org}/exceptions`,
      `POST /api/v1/orgs/{org}/repos/{repo}/work/{key}/decompose`, and
      `GET /api/v1/orgs/{org}/repos/{repo}/work/{key}/subtasks`.
- [ ] Write the failing test `internal/reviews/exceptions_test.go`:
      `func TestSummaryCountsEachCategoryOnce(t *testing.T)` seeds one run in each of the six
      states and asserts every counter is exactly 1;
      `func TestRunWithFailedGateIsAnException(t *testing.T)` asserts a failing gate places the
      run in the exception list with a reason naming the gate;
      `func TestHealthyAutoMergeableRunIsNotAnException(t *testing.T)` asserts a green
      auto-mergeable run appears in `ReadyToAutoMerge` and not in the exception list;
      `func TestSummaryIsOrgScoped(t *testing.T)` asserts org A's summary counts none of org B's
      runs. Run `go test ./internal/reviews/` — expect FAIL with "undefined: reviews.Exceptions".
- [ ] Implement `Exceptions` as one query per category with an explicit `org_id` predicate,
      union-ed in Go, so a missing predicate cannot widen the result.
- [ ] Run `go test ./internal/reviews/` and re-run `TestEveryRouteIsInOpenAPI` — expect PASS.
- [ ] Commit as `feat: add exception dashboard data`.

## Task 9: End-to-end software factory test

Files: `tests/e2e/factory_test.sh`, `deploy/helm/novaforge/values.yaml`

Interfaces: consumes the deployed stack from every prior plan; adds the values keys
`factory.autoMerge.enabled`, `factory.autoMerge.maxFilesChanged`, and
`factory.maintenance.intervalHours`.

- [ ] Write the failing test `tests/e2e/factory_test.sh`: on the kind cluster, create an epic
      Work Item, run `nf work decompose NF-1`, assert at least three subtasks exist with
      dependency ordering, assert only the dependency-free subtasks acquire Agent Runs within
      180s, mark the first subtask's run successful, assert its dependent then starts, assert the
      resulting run carries verdicts from at least two distinct reviewer roles neither of which
      is the author agent, assert a run with a failing gate is not auto-merged, and finally seed
      a vulnerable dependency and assert a maintenance Work Item appears with no Agent Run
      attached. Run `bash tests/e2e/factory_test.sh` — expect FAIL with "Error: unknown command
      decompose".
- [ ] Add the `nf work decompose`, `nf work subtasks`, and `nf dashboard` commands to the CLI
      following the structure established in the foundation plan's Task 17.
- [ ] Extend `values.yaml` with the three factory keys, defaulting `autoMerge.enabled` to `false`
      so a fresh installation never merges without an explicit opt-in.
- [ ] Run `bash tests/e2e/factory_test.sh` — expect PASS.
- [ ] Commit as `feat: add end-to-end software factory test`.
