# Forks and cross-fork Engineering Runs (S-30)

Status: closed 2026-09-25
Created: 2026-09-25

## Description

Engineering Runs are branch-based within one repository, so there is no fork-and-
propose flow. This changes the run model, so it comes after repository
administration and access control are settled.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [x] `internal/gitops TestForkCarriesHistoryAndIsolatesWrites` passes: a fork has the
      parent's commits, records its parent id, and a push to the fork leaves the parent's
      refs unchanged.
- [x] `internal/reviews TestCrossForkRun` passes: a run whose source is a fork and whose
      target is the parent is reviewed and merged by the same gate and approval rules as a
      branch run, with no path that skips them.
- [x] `reviews.Merger` fetches the source ref from `source_repo_id` into the target's
      throwaway clone, reusing the existing merge path rather than adding a second one.
- [x] A run's source repository defaults to its own repository, so every existing run
      keeps working.
- [x] The Repos and RunDetail screens show the fork relationship; type-check and build
      clean.

## Evidence

All runs against a disposable database created and dropped by
`hack/owned-db-test.sh`.

- `internal/gitops TestForkCarriesHistoryAndIsolatesWrites` PASS (1.53s).
- `internal/reviews TestCrossForkRun` PASS (4.52s).
- Criterion 2 is the one worth the most scrutiny, and as written the test did not
  earn it. It asserted only that the merge was refused while the parent's required
  gate was unsatisfied — but a cross-fork run is refused for several reasons, and
  "the gates could not be read at all" is indistinguishable from "the parent's
  gate was not satisfied". Making the run lookup report the fork as the run's
  repository (`repoID = srcRepoID` in `gates.NewServiceRunLookup`) still refused
  the merge, so nothing about whose definitions applied was pinned. The test now
  asserts the refusal names `api-compatibility`, the gate the PARENT declared,
  which is only possible if that definition was resolved. Verified red with that
  break in place: "unknown ref or path" instead. Restored, green again.
  While doing this I also established a stronger guarantee than the code claimed:
  `Resolve` is given the target branch's SHA, not a ref name, so definitions are
  pinned by content address — a fork cannot substitute its own even if the lookup
  named it, and a fork lacking that object fails closed.
- Criterion 3 verified by breaking it: setting `SourceRepo: ""` on the
  `MergeRequest` in `Merger.Merge` turned `TestCrossForkRun` red with
  'unknown ref "contribution" or "main"'. Restored, green again. There is one
  merge path; `mergeRefs` gained a single `sourceRepoPath` parameter and the fork's
  ref is fetched into the throwaway clone, never into the bare target — which would
  publish a ref before any gate had authorized it.
- Criterion 4 was NOT pinned by anything, and now is. Removing the
  `COALESCE(source_repo_id, repo_id)` from both read queries broke no test: the
  Go-side `Run.sourceRepo()` default masked it. The case the COALESCE protects is a
  rolling deploy, where an older binary inserts a run against the migrated schema
  without the column; such a row must read back as a run in its own repository,
  because a nil source repository makes the gate lookup resolve a repository that
  does not exist and every legacy run's gate evaluation fails. Added
  `TestRunWithNoSourceRepositoryReadsAsItsOwn`, which inserts that row with raw
  SQL (CreateRun cannot produce the state) and asserts both `GetRun` and
  `ListRuns`. Verified red without the COALESCE:
  "source repository = 00000000-0000-0000-0000-000000000000". Restored, green.
- Criterion 5: `Repos.tsx` has a Forks panel naming the parent and the forks
  pointing at this repository, and `RunDetail.tsx` prefixes a cross-fork run's
  source ref with the fork's name. Neither invents a name: a parent outside the
  workspace scope is shown as a parent that cannot be named, not as no parent.
  `npx tsc -b --noEmit` exits 0 and `npm run build` succeeds.
- `gofmt -l internal cmd` prints nothing; `go build ./...` and `go vet ./...` exit 0.

### Not proven, and why

- **Nothing here has run on the cluster.** The Nexus registry's write path returns
  HTTP 500 to every blob upload, so no image could be pushed at this commit and the
  deployment still runs `b84ffec`.
- **A cross-fork run cannot satisfy a gate that reads the change's tree.** A gate
  runner materializes the workspace from the target repository, where a fork's
  objects are absent until the merge fetches them. Such a run is refused, never
  waved through, and `TestCrossForkRun` asserts that refusal. The successful
  cross-fork merge in the test is against a target declaring no gate definitions.
  So the criterion is met for authority and refusal; a cross-fork run that must
  pass a content-reading gate is not yet possible.
- **Forking into another organization is refused**, deviating from the plan's
  `toOrg`. Copying one organization's entire history into another is authorized by
  neither side, so `to_org` is validated rather than ignored, and cross-fork runs
  stay within one organization.
- No e2e script covers forks, and `nf` has no fork command.
