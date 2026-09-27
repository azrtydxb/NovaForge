# Gap closure G03: durable run and CI webhooks

Status: closed 2026-09-27
Created: 2026-09-27

## Description

Publish Engineering Run transitions and terminal aggregate CI outcomes from their
owning services. Deliver signed, scoped notifications with stable IDs and durable
retry limits, including recovery after Redis outages and worker restarts.

## Acceptance criteria

- [x] Committed run transitions and CI results create durable publication intent; rolled-back changes do not.
- [x] Service entrypoints relay their own outboxes to Redis without cross-schema reads.
- [x] Hooks receive selected events with stable IDs and signatures; other organizations receive nothing.
- [x] Retry bounds survive worker restarts and committed events survive Redis outages.
- [x] GUI/API identify the supported events and cluster acceptance exercises actual transitions.

## Evidence

- Owner-schema SQL outboxes for Engineering Run creation/state changes and terminal aggregate CI results are relayed by cmd/work-reviews and cmd/ci-runner. Versioned stable IDs feed durable hook dispatch/retry history.
- TestRunAndCIWebhookOutbox and TestWebhookRetryBudgetSurvivesRestart passed against real PostgreSQL/Redis and an actual signed receiver, covering rollback, outage, duplicate publication, tenant scope and restart retry bounds.
- git_host acceptance passed on ba12eff: signed push, Engineering Run and successful aggregate CI notifications reached the approved receiver. A discovered runner completion/log race was fixed and verified by delayed real gRPC stream admission (runner-receipt.log).
- GUI event selector/help and API contract expose push, engineering_run and ci_result, with stable delivery IDs across retries. Cluster log: /tmp/e2e.git_host.log; summary: targeted-release-acceptance.log.
