# Gap closure G05: durable blob cleanup and ownership

Status: open
Created: 2026-09-27

## Description

Reclaim LFS and release payloads durably after deletion, preserving concurrent uploads, fork references and transferred repository ownership. Supply a bounded report for historical orphans before any reclamation.

## Acceptance criteria

- [x] Failed duplicate uploads preserve existing content; transfers retain new-owner access and refuse old-owner reads.
- [x] Forks retain payloads after parent deletion; removing the last reference reclaims the object in real MinIO.
- [ ] Upload interruptions and storage failures leave retryable cleanup records; concurrent uploads are fenced.
- [ ] Release and organization/repository cascade deletion queue exact physical keys.
- [ ] Historical orphan inspection is bounded and report-only by default.
- [ ] Production worker is deployed and exercised by cluster acceptance.

## Evidence

- `source hack/env.sh; go test ./internal/gitops -count=1`: passed, 44.920s, after migration/worker changes.
- `go test ./internal/gitops -run TestBlobOwnershipAndDurableCleanup -count=1 -v`: passed; real PostgreSQL and MinIO, transfer/fork/deletion and interrupted upload assertions.
