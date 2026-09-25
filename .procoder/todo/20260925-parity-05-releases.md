# Releases with assets (S-28)

Status: closed 2026-09-25
Created: 2026-09-25

## Description

CI artifacts exist; releases do not. A tag cannot carry downloadable assets, so
there is no way to publish a build the way a team expects from a Git host.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [x] `internal/gitops TestReleaseWithAssets` passes: a release on an existing tag carries
      an uploaded asset that downloads byte for byte, a release on a nonexistent tag is
      refused, and deleting a release deletes its blobstore objects.
- [x] Asset keys are scoped `org/<org_id>/repo/<repo_id>/release/...` so an object cannot
      be reached from another organization by guessing a name, proven by a test.
- [x] Asset download streams rather than buffering the whole object in memory.
- [x] Routes under `/orgs/{org}/repos/{repo}/releases` exist and `make openapi` leaves no
      diff.
- [x] The Repos screen lists releases and their assets; type-check and build clean.

## Evidence

Verified by me, not taken from the agent's report:

- `go test ./internal/gitops/` — the whole package ok (54.8s), including
  `TestReleaseWithAssets`: a release refused on a tag that does not exist, a 1 MiB asset
  downloaded byte for byte, and the blobstore objects confirmed gone from MinIO after
  deleting the release.
- `TestReleaseAssetKeysAreOrgScoped` asserts the exact key
  `org/<org>/repo/<repo>/release/<release>/<asset>`, that two organizations sharing a
  repository name, tag and asset name get different keys, and that cross-organization
  list, add and delete are all refused.
- Red-green on the streaming claim: widening the download chunk to 64 MiB made
  `TestReleaseAssetStreamsThroughTheRPC` fail with "a 1048576-byte asset was sent in 1
  message(s): it is being buffered, not streamed". Restored and green. The agent also
  reported the same method proving the upload framing and the edge layer.
- `make openapi` leaves no diff on a second run after merging with the collaborator and
  HTTPS routes. `npx tsc -b --noEmit` clean, `npm run build` succeeded (456.88 kB).
- `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` clean on the merged tree.

A seam the agent found and fixed, worth recording: `cmd/git-platform` installed only
the unary interceptor, so a streaming RPC saw no caller and refused everything. A
release download is this service's first streaming RPC, so the feature would have been
written, tested in-process and dead in the pod. It installed
`svcauth.StreamServerInterceptor`.

Known and NOT proven, stated rather than hidden:

- None of the `cmd/git-platform` wiring — blobstore construction, the stream
  interceptor, creating the `novaforge-releases` bucket — has run in a pod. Nothing was
  built or deployed. Given this repository's history that wiring is exactly where a
  remaining seam would be, so releases should be exercised against the cluster before
  anyone calls the feature done.
- `internal/platformtest` starts git-platform without a release store, so anything run
  through that stack sees releases as unavailable.
- There is no e2e script for releases, and the `nf` CLI has no release support.
