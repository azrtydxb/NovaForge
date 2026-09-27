# Gap closure G05: durable blob cleanup and ownership

Status: closed 2026-09-27
Created: 2026-09-27

## Description

Reclaim LFS and release payloads durably after deletion, preserving concurrent uploads, fork references and transferred repository ownership. Supply a bounded report for historical orphans before any reclamation.

## Acceptance criteria

- [x] Failed duplicate uploads preserve existing content; transfers retain new-owner access and refuse old-owner reads.
- [x] Forks retain payloads after parent deletion; removing the last reference reclaims the object in real MinIO.
- [x] Upload interruptions and storage failures leave retryable cleanup records; concurrent uploads are fenced.
- [x] Release and organization/repository cascade deletion queue exact physical keys.
- [x] Historical orphan inspection is bounded and report-only by default.
- [x] Production worker is deployed and exercised by cluster acceptance.

## Evidence

- Migrations retain immutable physical keys and enqueue exact-key cleanup before metadata disappears, including release assets and repository/organization cascades. The production Git service drains the durable queue.
- Real PostgreSQL/MinIO ownership tests passed: rejected duplicate upload preserves bytes, transfer refuses the old owner, forks preserve shared payloads, last-reference deletion reclaims, and interrupted uploads are reclaimed.
- TestBlobCleanupSurvivesStorageDisconnectAndWorkerRestart passed through a real MinIO forwarding endpoint that was disconnected: retry count/error persisted; an independent worker skipped a locked upload and reclaimed the object after connectivity returned.
- Deployed git_host acceptance on ba12eff proved a fresh fork clone still fetches payloads after parent deletion, final-reference collection, safe repository-name reuse and physical collection after organization deletion.
- cmd/blob-audit bounds organization, pagination and >=24-hour grace; default report-only, optional durable enqueue, no direct historical sweep. Operations guide documents use and pending cleanup. Logs: blob-outage.log, full-regression.log and /tmp/e2e.git_host.log.
