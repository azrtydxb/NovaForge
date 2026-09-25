# Git LFS (S-25)

Status: open
Created: 2026-09-25

## Description

There is no LFS code, so a repository with large binaries cannot be used at all. The
blobstore already holds CI artifacts and can hold LFS objects, and the batch API
rides the existing Git transport.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [ ] `internal/gitops TestLFSRoundTrip` passes: an unmodified `git lfs` client pushes a
      large file and a fresh clone retrieves its bytes exactly, with the Git repository
      holding the pointer and not the payload.
- [ ] `TestLFSQuotaAndOwnership` passes: an object is unreachable with another
      organization's credential, and an upload declaring more than
      `NF_LFS_MAX_OBJECT_BYTES` is refused before any bytes are stored.
- [ ] LFS authorizes through the same credential path as the Git transport, so it cannot
      be a way around repository access.
- [ ] `NF_LFS_MAX_OBJECT_BYTES` is added to the config, the chart values and the service
      template, and is read — a config field nothing reads is this repository's second
      most common defect.
- [ ] `go test ./...` no failures; the round-trip test skips with a clear message when
      `git lfs` is absent rather than passing silently.

## Evidence

<!-- Filled at close time. -->
