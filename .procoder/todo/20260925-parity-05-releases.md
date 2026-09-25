# Releases with assets (S-28)

Status: open
Created: 2026-09-25

## Description

CI artifacts exist; releases do not. A tag cannot carry downloadable assets, so
there is no way to publish a build the way a team expects from a Git host.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [ ] `internal/gitops TestReleaseWithAssets` passes: a release on an existing tag carries
      an uploaded asset that downloads byte for byte, a release on a nonexistent tag is
      refused, and deleting a release deletes its blobstore objects.
- [ ] Asset keys are scoped `org/<org_id>/repo/<repo_id>/release/...` so an object cannot
      be reached from another organization by guessing a name, proven by a test.
- [ ] Asset download streams rather than buffering the whole object in memory.
- [ ] Routes under `/orgs/{org}/repos/{repo}/releases` exist and `make openapi` leaves no
      diff.
- [ ] The Repos screen lists releases and their assets; type-check and build clean.

## Evidence

<!-- Filled at close time. -->
