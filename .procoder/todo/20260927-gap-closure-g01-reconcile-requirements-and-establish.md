# Gap closure G01: reconcile requirements and establish baseline

Status: open
Created: 2026-09-27

## Description

Reconcile superseded scope and verification claims before changing implementation.
Establish a fresh kw and real-datastore baseline for the gap-closure plan.

## Acceptance criteria

- [ ] The spec reconciles GUI integration, import/mirroring and SSH-authenticated LFS with later requirements.
- [ ] Current status distinguishes the September 26 successful suites from the earlier model outage.
- [x] Live deployment and remote state are checked and recorded.
- [ ] Full Go baseline records actual failures and skips with datastore prerequisites loaded.

## Evidence

- 2026-09-27: kubectl on kw reports all 12 novaforge pods Running/Ready, zero restarts; nine application deployments use dc5f70f. Helm reports revision 9 deployed.
- git fetch origin succeeded; local main f314f75 is 38 commits ahead of origin/main.
- Baseline test evidence: /tmp/novaforge-gap-20260927/baseline-go.jsonl and baseline-go.stderr (running).
