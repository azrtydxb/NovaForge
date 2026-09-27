# Gap closure G02: cross-fork gate evaluation

Status: closed 2026-09-27
Created: 2026-09-27

## Description

Materialize a fork's source revision while retaining the parent's policy and
approval authority. Carry the distinction through diffs, dependency reads,
API compatibility, cached evidence and the user-visible diff.

## Acceptance criteria

- [x] A cross-fork change passes a real executable parent gate and merges after independent approval.
- [x] A failing fork change and a fork that weakens policy are refused under the parent's rules.
- [x] Change classification and API baselines read the correct repositories; neither bare repository is mutated by diff/evaluation.
- [x] Source or policy revision changes invalidate old results; cross-org reads are refused.
- [x] GUI displays the cross-fork diff through the REST/OpenAPI contract.
- [x] A cluster acceptance suite proves the production workflow on the deployed commit.

## Evidence

- Core implementation 864ff23/9733e2d carries source repository identity through RunHead, workspace materialization, change classification, API baseline reads and REST/GUI diffs. Temporary cross-repository diff fetches do not import objects into the bare parent; merge uses a separate tracking ref instead of overwriting origin/main.
- Real PostgreSQL/Git/kw sandbox TestCrossForkExecutableGate passed: a fork cannot weaken the parent's required executable gate; broken code fails, fixed code passes, stale authority is refused, and independent approval permits merge.
- TestCrossRepositoryDiffDoesNotImportObjects passed, including foreign organization and ref-option/path handling; complete gitops and gates regressions passed.
- Deployed acceptance at 9733e2d: merge_test.sh step 7 passed, proving cross-fork diff, parent executable gate, independent approval and same-named main-to-main merge. Log: /tmp/e2e.merge.log and core-acceptance-recheck.log.
- Browser qualification on 2bb3a65: UI-created browser-fork contributes a new commit; browser-import Run #1 Changes displays browser-fork:main → main and the exact added README text, with one-file/+2-line impact. Actual browser text retained in /tmp/novaforge-gap-20260927/browser-fork.txt.
