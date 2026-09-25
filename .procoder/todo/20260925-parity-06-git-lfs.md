# Git LFS (S-25)

Status: closed 2026-09-25
Created: 2026-09-25

## Description

There is no LFS code, so a repository with large binaries cannot be used at all. The
blobstore already holds CI artifacts and can hold LFS objects, and the batch API
rides the existing Git transport.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [x] `internal/gitops TestLFSRoundTrip` passes: an unmodified `git lfs` client pushes a
      large file and a fresh clone retrieves its bytes exactly, with the Git repository
      holding the pointer and not the payload.
- [x] `TestLFSQuotaAndOwnership` passes: an object is unreachable with another
      organization's credential, and an upload declaring more than
      `NF_LFS_MAX_OBJECT_BYTES` is refused before any bytes are stored.
- [x] LFS authorizes through the same credential path as the Git transport, so it cannot
      be a way around repository access.
- [x] `NF_LFS_MAX_OBJECT_BYTES` is added to the config, the chart values and the service
      template, and is read — a config field nothing reads is this repository's second
      most common defect.
- [x] `go test ./...` no failures; the round-trip test skips with a clear message when
      `git lfs` is absent rather than passing silently.

## Evidence

Verified by me, not taken from the agent's report:

- `go test ./internal/gitops/ -run TestLFS -v` — `TestLFSRoundTrip` PASS with a real
  git-lfs 3.8.0 client, and all six `TestLFSQuotaAndOwnership` subtests PASS: an upload
  over the limit refused before any bytes are stored, a transfer over the limit refused
  at the transport, a body that does not hash to its oid refused, the upload authorized
  as a write against the declared ref, the owner downloading the bytes, and another
  organization's credential reaching nothing.
- The round trip's proof is better than an assertion about pointers: after pushing a
  5 MiB random file it checks the whole bare repository is under 1 MiB. Random data does
  not compress, so that is evidence the payload is not in the git directory.
- `go test ./internal/gitops/ ./internal/service/` ok; `gofmt -l internal cmd` empty;
  `go build ./...` and `go vet ./...` clean after merging with the collaborator work.
- The agent reported using the tree's own guards as red proofs:
  `TestLoadConfigPopulatesEveryField` caught `LFSMaxObjectBytes` declared but unread, and
  `chart_test.go` caught the Deployment not setting `NF_LFS_MAX_OBJECT_BYTES`.

A gap I found in its work and closed:

- The reasoning for scoping the blob key by organization and repository is sound —
  content-addressing by oid alone would let a guessed hash reach another organization's
  bytes, and one deletion would take every copy — but nothing pinned it. Stripping the
  scoping broke no test, because what actually refuses a request is the org-scoped row
  lookup. `TestLFSBlobKeyIsScopedToItsRepository` now pins the key, verified red by
  content-addressing it.

Met for HTTP, and NOT the whole of S-25:

- **LFS is served over HTTP only.** There is no `git-lfs-authenticate` on the SSH
  transport, so a clone reaching the platform over `ssh://` will not fetch LFS objects.
  S-25/TestLFSRoundTrip is recorded as **partial** in traceability for this reason.
- The batch `verify` operation and non-basic transfer adapters are refused with a 422
  saying so rather than implemented.
- Deleting a repository or organization leaves its LFS payloads in the bucket: the rows
  cascade, the objects do not. This matches the existing behaviour for release assets and
  was not widened to fix either.
- Nothing ran on the cluster, and there is no GUI surface — LFS is a transport protocol,
  though an LFS usage figure on the repository screen would be a reasonable follow-up.
- The agent installed git-lfs 3.8.0 via Homebrew on this workstation. Without it the
  round trip skips with a message saying it is not proven, rather than passing silently.
