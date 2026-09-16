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
  architecture proposal. CI history, coverage, benchmark and graph inputs
  still need production wiring; scanner unit tests are not sufficient.
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

## Next implementation: CI test history input

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

## Evidence location

Latest deployed batch: maintenance policy isolation at revision 74 (b75d8d9)
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
