# Forks and cross-fork Engineering Runs (S-30)

Status: open
Created: 2026-09-25

## Description

Engineering Runs are branch-based within one repository, so there is no fork-and-
propose flow. This changes the run model, so it comes after repository
administration and access control are settled.

Plan: `.procoder/plans/git-parity.md`

## Acceptance criteria

- [ ] `internal/gitops TestForkCarriesHistoryAndIsolatesWrites` passes: a fork has the
      parent's commits, records its parent id, and a push to the fork leaves the parent's
      refs unchanged.
- [ ] `internal/reviews TestCrossForkRun` passes: a run whose source is a fork and whose
      target is the parent is reviewed and merged by the same gate and approval rules as a
      branch run, with no path that skips them.
- [ ] `reviews.Merger` fetches the source ref from `source_repo_id` into the target's
      throwaway clone, reusing the existing merge path rather than adding a second one.
- [ ] A run's source repository defaults to its own repository, so every existing run
      keeps working.
- [ ] The Repos and RunDetail screens show the fork relationship; type-check and build
      clean.

## Evidence

<!-- Filled at close time. -->
