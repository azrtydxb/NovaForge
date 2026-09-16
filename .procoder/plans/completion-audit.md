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
- S-20: verify all eight maintenance detectors, not just a CVE test.
- S-7: workspace reaper lifetime versus configured run lifetime; cost budget proof.
- S-9: airgap evidence must distinguish runtime confinement from gateway egress.
- Failure modes: artifact upload and decode errors now fail the job (d6e07d1),
  with red/green regressions. Still audit absent/truncated artifact payloads and
  final log capture failures.
- Operational targets: no enterprise-scale proof may be inferred from small tests.

## Evidence location

Current full Go suite: /tmp/novaforge-full-go-test.log.
Current full cluster suite supervisor: /tmp/novaforge-full-e2e.log and
/tmp/novaforge-full-e2e.exit; per-suite logs /tmp/e2e.<suite>.log.
These temporary files support investigation; durable conclusions belong in
BUILD-STATUS.md and the spec traceability record.
