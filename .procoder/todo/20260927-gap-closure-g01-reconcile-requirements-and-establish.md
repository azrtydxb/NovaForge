# Gap closure G01: reconcile requirements and establish baseline

Status: closed 2026-09-27
Created: 2026-09-27

## Description

Reconcile superseded scope and verification claims before changing implementation.
Establish a fresh kw and real-datastore baseline for the gap-closure plan.

## Acceptance criteria

- [x] The spec reconciles GUI integration, import/mirroring and SSH-authenticated LFS with later requirements.
- [x] Current status distinguishes the September 26 successful suites from the earlier model outage.
- [x] Live deployment and remote state are checked and recorded.
- [x] Full Go baseline records actual failures and skips with datastore prerequisites loaded.

## Evidence

- Baseline kw inspection: all 12 pods Ready with zero restarts; all nine application deployments dc5f70f, Helm revision 9. Refreshed origin; initial main f314f75 was 38 commits ahead.
- Spec reconciled GUI, S-27 import/mirroring and SSH-authenticated HTTPS LFS; September 25 gateway failure remains historical after September 26 acceptance, rather than a current failure claim.
- Full real-datastore baseline: 42 packages passed, three failed transient connection tests, 21 had no tests; 1603 tests passed, three failed, four explicitly skipped. Each of the three failures passed its targeted rerun (baseline-recheck.log).
- Skips: OpenBao binary/disposable-provider lane not configured; live model decomposition needs AI_ENDPOINT/AI_MODEL; interactive GUI fixture opt-in; namespaced UID fault requires an owned database. The last was subsequently run with hack/owned-db-test.sh and passed.
- Fresh full regression after implementation: all 46 tested packages passed, no failures (full-regression.log); additional runner-receipt and blob-disconnect regressions passed.
- Logs: /tmp/novaforge-gap-20260927/baseline-go.jsonl, baseline-recheck.log, full-regression.log, owned-uid.log. No skipped test is counted as deployed proof.
