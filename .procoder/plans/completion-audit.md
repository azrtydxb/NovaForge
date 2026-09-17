# Whole-platform completion audit

Requested by the user on 2026-09-16: complete all pending features, milestones,
epics and tasks, not just the most recent regression.

## Completion rule

Do not infer implementation completion from a closed task or a passing component
test. Reconcile the design, backend spec, six implementation plans, production
callers, GUI and cluster evidence. Preserve explicit spec exclusions; distinguish
product gaps from deployment choices and external prerequisites. Formal task
closure uses the human-invoked Procoder workflow, never edited Status fields.

## Inventory and execution

- [x] Read the design and backend spec.
- [x] Inventory task records: 67 closed; no backlog or sprint directory exists.
- [x] Recover Helm and verify live logs, Git and GUI at revision 69 (1c3c53f).
- [x] Run all Go packages with hack/env.sh sourced: exit 0.
- [x] Run all twelve in-cluster acceptance suites and repair failures: all pass
      on revision 70 (d6e07d1). Factory failed on revision 69; schema enum fixed.
- [ ] Audit all six plans against production callers and actual acceptance evidence.
- [ ] Reconcile every BUILD-STATUS.md known limitation with spec requirements.
- [ ] Audit GUI functionality and unavailable endpoints against the design.
- [x] Review/remove speculative timeout changes from the preceding diagnosis:
      removed MinConnectTimeout overrides; retained the RPC deadline and added a
      regression proving it is bounded without canceling the handler context.
- [ ] Run frontend build, types, security, lint and full verification on final code.
      Current audit batch: frontend build/types pass; security/lint have no findings;
      full Go suite passes with dev datastores. Procoder test also passes after making
      the SAST test use the committed ruleset by default.
- [ ] Update traceability and evidence without overstating component coverage.
- [ ] Build/deploy final implementation with complete immutable image set.
- [ ] Run final gate, commit and push verified work.

## Gaps requiring implementation review

These are not accepted deferrals. The audit must establish whether and where the
requirements are implemented, then add missing paths and regression evidence.

- S-11: deployment approval actions exist, but BUILD-STATUS says no deploy action.
- S-12: secret redemption expires, but the delivered credential remains durable.
- S-14/S-15: graph relationship coverage and indexing languages/tools are narrower
  than the spec wording (Tree-sitter, LSP, SCIP).
- S-18: verify every repository configuration directory is consumed.
- S-20: the sweep now resolves default-branch gate definitions and supplies
  architecture parameters; the real Git/database regression was red before
  wiring and green afterwards. The factory e2e now asserts an unapproved
  architecture proposal. Go JSON CI history is wired and cluster-proven at
  revision 75. Benchmark artifacts are now wired with real-service regressions;
  deployed at revision 77 with all twelve suites passing. Bounded Go graph inputs
  are wired and cluster-proven at revision 81; broader indexing remains open.
  Coverage is wired and cluster-proven at revision 78;
  all twelve suites passed.
- S-7: workspace expiry now travels from agent-runtime through namespace
  provisioning to the reaper (d0069b2); configured long runs are protected.
  No-limit runs retain the existing 12-hour credential ceiling. Cost budget
  proof and cluster lifetime evidence remain to be audited.
- S-9: airgap evidence must distinguish runtime confinement from gateway egress.
- Failure modes: artifact upload and decode errors now fail the job (d6e07d1),
  with red/green regressions. Missing/truncated payloads, partial missing paths,
  path whitespace and masked tar errors are fixed in d0069b2. Final log read
  failure now fails the job in c4ff7e7, with a red/green regression and a passing
  work_ci run on revision 72.
- Operational targets: no enterprise-scale proof may be inferred from small tests.

## CI test history input — implemented and deployed

- Wire the existing CI gRPC client into the production maintenance sweeper in
  `cmd/work-reviews/main.go` and `internal/maintenance/sweep.go`.
- Read completed shell-job logs through CI's authenticated API, parsing Go
  `go test -json` pass/fail test events. Never infer individual test results
  from a job exit status, package failure, skipped test or arbitrary text.
- Keep job/package/test identity and exact commit SHA; isolate history-read
  failures as flaky-scanner errors. Bound reads to recent runs and a deadline.
- Extend the real Git/database maintenance regression with CI/PostgreSQL,
  Redis and MinIO evidence, then verify production wiring and cluster acceptance.
- This input supports Go JSON test output, not arbitrary test report formats;
  coverage, benchmarks and graph inputs remain separate open work.

## CI dispatch reservation correction — deployed

The broker-down regression observed a job marked running before credentials
were resolved. Trace `internal/ci/pump.go`, `store.go` and `dispatch.go`;
separate pending reservation (existing runner_id) from the running transition.
Test the broker-call interval deterministically with the real credential stack,
prevent double claims, preserve disconnect cleanup and terminal states, and
retain the strict broker-down assertion. No new public job status is needed.
Implemented in `b3d2b3f`, deployed at revision 76. The deterministic held-broker
regression was red then green; removing the terminal-state guard made the
late-response case fail. Unchanged broker-down test passed three repeats,
CI passed under `-race`, full uncached Go suite passed, and all twelve cluster
suites passed. Evidence: /tmp/novaforge-reservation-{race,tests,e2e,deploy}.log.

## Benchmark evidence input — implementation scope

Use the approved previous-comparable-successful-default-branch baseline policy.
Read `benchmarks.txt` artifacts through the CI gRPC API, never cross-schema SQL.
Use the installed Go benchmark parser; require OS, architecture, CPU, toolchain,
package and explicit environment metadata to match. Compare lower-is-better
ns/op, B/op and allocs/op independently, using medians for repeated observations.
Reject malformed/non-finite measurements and bounded-download violations.
Missing latest evidence or baseline must surface as unavailable scanner errors.

Files: `internal/maintenance/benchmarks*.go`, `sweep.go`, real CI artifact tests,
and `tests/e2e/work_ci_test.sh`. Prove a real benchmark allocation regression
through two successful CI runs and an unapproved Work Item. Keep coverage and
graph input work open; this implements the performance input only.

Implemented with real Go benchmarks, Git, CI/PostgreSQL/Redis/MinIO; integration
and mutation regressions are red/green. Full Go and Procoder test (39 packages)
pass; affected packages pass under `-race`. The committed work_ci fixture failed
on revision 76 with no performance proposal, then passed after deployment of
`4f7ee61` at revision 77. All twelve cluster suites passed with complete immutable
images and the normal Helm preflight.
Testing found shared-table truncation in `ciPoolExclusive`; isolated databases
replace it. Their teardown exposed leaked dedicated migration connections,
fixed with explicit ownership and proven by pg_stat_activity on success/failure.
See BUILD-STATUS.md and /tmp/novaforge-benchmark-{race-fixed,tests-confirm}.log.

## Coverage evidence input — current implementation scope

Preserve structured coverage and repository identity in gates-owned evaluations;
add an org-scoped latest-two tests-evaluation RPC. Compare successive recorded
head evaluations, preserving unavailable samples rather than skipping failures.
Read statement counts from the actual Go coverage profile, not rounded display
strings. Wire the authenticated gates client into both maintenance entry points.
Keep idempotent evaluation caching and existing cleanup ownership. Legacy rows
without repository/measurement metadata remain unavailable, with no guessed
backfill. Files: analysis coverage tests, gates runner/store/migration/proto/RPC,
maintenance input and production wiring. Verify measured 100% -> 50% through real
services, missing evidence and organization isolation, then gate/build/deploy and
cluster acceptance. Graph inputs and other broader requirements remain open.

Implemented in `18a9b6c`, deployed at Helm revision 78. Real Go-profile precision/loss tests and
maintenance wiring were red then green; scope and serialization mutations also
failed as intended. Full uncached Go, affected-package race tests, Procoder test
(39 packages) and buf lint pass. The committed merge fixture failed on revision
77 with no coverage proposal, then passed at revision 78 along with all twelve
suites. Complete immutable images and normal Helm preflight were used. No task
is closed or whole-platform completion inferred.

## Graph maintenance — safety corrections, integration still open

Regression tests exposed four prerequisites before enabling production graph
maintenance: the scanner ignored the indexer's depends_on edges, documentation
lookup was not repository-scoped, both scanner queries trusted input org ids
without checking caller scope, and edge writes accepted foreign endpoints.
Queries now belong to `internal/graph/maintenance.go`; both endpoints are
validated atomically by the shared edge writer, including transactional file
replacement. Unauthorized replacements roll back rather than destroying the
previous graph. Findings describe candidates, not proof that deletion is safe.
Real PostgreSQL regressions were red then green; graph/maintenance/ctxasm race
suites and the full uncached Go suite passed. Procoder test passed 39 packages;
lint/security reported zero findings. Commit `f4b52f9` deployed at revision 79;
all twelve cluster suites passed, with normal immutable-image preflight.

Remaining production work: expose graph-owned evidence through authenticated
RPCs, wire `Sweeper` without a graph database connection, extract explicit
context-document symbol references, and verify index completeness/freshness at
the scanned revision before interpreting absence. Unsupported languages and
entry points must not become fabricated dead-code findings. Add real-service
and cluster proposals with approval assertions. Graph cleanup also omitted
`file_references`; both existing purge paths now remove only their scoped
references. The real PostgreSQL regression failed before the fix and covers
repository/organization boundaries and repeated cleanup.
The graph-input criterion remains unchecked despite the safety corrections.
A full-suite benchmark fixture failure (measured 48 B/op versus assumed 32)
also led to exact measurement and CI run-id assertions rather than guessed
bytes. Removing compatibility matching fails the revised assertion. Full
uncached Go and affected race suites passed afterward; Procoder test passed
39 packages. Evidence: /tmp/novaforge-graph-{tests-confirm,race-confirm,
benchmark-mutation,deploy,e2e}.log.

## Index recovery prerequisite — deployed at revision 80

Before exposing graph absence as maintenance evidence, correct its write path.
`IndexCommit` acknowledged partial fetch/model/store failures and saved the SHA,
so redelivery skipped the lost work. The real Git/authenticated RPC/PostgreSQL
regression failed on both acknowledgment and recovery before correction.
Healthy files still progress, but failed paths now return an error and prevent
checkpoint advancement. Production `HandlePush` takes a transaction-scoped
repository lock across replicas, resolves the current default-branch head, and
reconciles current plus previously indexed paths after a discontinuity. Late
old events cannot restore deleted code. The prior checkpoint is invalidated
before writes so partial state cannot look complete after a force push back.

Files: `internal/indexing/{indexer,reconcile,retry_test}.go` and the existing
indexer tests. The late-event regression was red before reconciliation. Removing
the lock, full reconciliation or checkpoint invalidation independently fails
tests. Real PostgreSQL/Git tests pass under race; the full uncached Go suite and
Procoder test passed. Commit `4cb2fe7` deployed at revision 80 with complete
immutable images and normal preflight; all twelve cluster suites passed.
The failure/recovery and lock cases are proven by real-service regressions,
not cluster fault injection. Evidence: /tmp/novaforge-index-{build,deploy,e2e}.log.

This is still not a completeness contract for maintenance: silent parser skips,
unusual Git path parsing, module-mapping changes, legacy checkpoints and a
revision-bound graph read API remain to be handled. Production maintenance
wiring/context references and the broader product gaps remain open. Do not
infer their completion from the index recovery regressions.

## Graph maintenance RPC — deployed at revision 81

- [x] Distinguish successful supported parsing from empty/skipped/recovered syntax.
- [x] Store exact source/root-module digests atomically with each file's graph.
- [x] Add authenticated graph-owned `MaintenanceSnapshot`, validating the full Go
      manifest and reading symbols/references in one database snapshot.
- [x] Resolve stored by-name references as well as materialized edges, avoiding
      false absence when concurrent file replacement delays edge materialization.
- [x] Pin maintenance checkout/policy to one SHA, wire production graph client,
      read explicit context-document symbol links and retain source evidence.
- [x] Real-service red/green proposal tests; parser/snapshot regressions; anonymous,
      incoming-credential and cross-org/repository checks; three restored mutations.
- [x] Full uncached Go, affected-package race and Procoder suite pass; lint/security
      have no blockers. Expanded committed cluster fixture failed revision 80
      with no proposals and passed revision 81.
- [x] Commit `626a383`, complete immutable image build, normal preflight/deploy;
      all twelve cluster suites passed at revision 81.
- [x] Deploy refresh of legacy evidence and unchanged files after root-module
      remapping (`de90ddd`, revision 82). Real-Git/PostgreSQL tests and three
      mutations verified; committed module-change fixture failed revision 81,
      passed revision 82, and all twelve suites passed. Legacy refresh is on the
      next push wake-up, not a startup-wide backfill.
- [ ] Reconcile unusual Git paths, audit lock-session-loss and deletion coordination.
- [ ] Extend beyond bounded root-module Go evidence; this is not LSP/SCIP completeness.

Missing/stale evidence stays unavailable, never a guessed clean result. The
current limits and remaining work are recorded in BUILD-STATUS.md. No broader
S-14/S-15/S-20 completion is claimed by this bounded path's acceptance.

## Evidence location

CI history input `bc51378` deployed at revision 75; all twelve suites passed:
/tmp/novaforge-history-e2e.log. Expanded work_ci failed against revision 74:
/tmp/novaforge-history-red-committed.log. Real Git/CI/PostgreSQL/Redis/MinIO
regression was red then green under `-race`. Full uncached Go suite passed:
/tmp/novaforge-history-tests-confirm.log. Prior run observed an intermittent
`TestBrokerDownFailsClosed` failure (running with a blocked detail rather than
pending); the reservation correction above fixes this without widening the
test tolerance.

Earlier deployed batch: maintenance policy isolation at revision 74 (b75d8d9)
passed all twelve in-cluster suites in one run. The malformed-policy regression
uses real Git/database services and was red then green under `-race`; the
cluster factory fixture verifies valid architecture-policy proposals, not
malformed policy. Complete images and normal Helm preflight were used.
Full Go suite with dev datastores passed:
/tmp/novaforge-maintenance-isolation-tests.log. Lint/security: zero findings.
Cluster evidence: /tmp/novaforge-isolation-e2e.log; deployment:
/tmp/novaforge-isolation-deploy.log. Approval to commit/deploy verified audit
batches is recorded in .procoder/ask/decisions.md; broader completion stays open.

Earlier: all twelve suites passed at revision 71 (d0069b2); work_ci and agent
passed at revision 72 (c4ff7e7). Procoder test passed (39 packages).

Earlier full Go suite: /tmp/novaforge-full-go-test.log.
Current full cluster suite supervisor: /tmp/novaforge-full-e2e.log and
/tmp/novaforge-full-e2e.exit; per-suite logs /tmp/e2e.<suite>.log.
These temporary files support investigation; durable conclusions belong in
BUILD-STATUS.md and the spec traceability record.
