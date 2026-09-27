# Gap closure G02: cross-fork gate evaluation

Status: open
Created: 2026-09-27

## Description

Materialize a fork's source revision while retaining the parent's policy and
approval authority. Carry the distinction through diffs, dependency reads,
API compatibility, cached evidence and the user-visible diff.

## Acceptance criteria

- [ ] A cross-fork change passes a real executable parent gate and merges after independent approval.
- [ ] A failing fork change and a fork that weakens policy are refused under the parent's rules.
- [ ] Change classification and API baselines read the correct repositories; neither bare repository is mutated by diff/evaluation.
- [ ] Source or policy revision changes invalidate old results; cross-org reads are refused.
- [ ] GUI displays the cross-fork diff through the REST/OpenAPI contract.
- [ ] A cluster acceptance suite proves the production workflow on the deployed commit.

## Evidence

- Source audit: RunLookup resolves the fork SHA but RunHead drops its repository;
  workspace and ClassifyChange read both revisions from the parent. GUI currently disables cross-fork diff.
