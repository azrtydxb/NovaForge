# factory 09: End-to-end software factory test

Status: open
Created: 2026-09-11

## Description

Plan step 9 of `.procoder/plans/factory.md`, which exists to: Close the loop from AI coding assistant to AI software engineering organization: decompose an epic into dependency-ordered subtasks across specialized agents, review changes with independent agents rather than their author, detect maintenance work autonomously and propose it as Work Items, and auto-merge only where policy already allows it.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `tests/e2e/factory_test.sh`, `deploy/helm/novaforge/values.yaml`

Interfaces: consumes the deployed stack from every prior plan; adds the values keys `factory.autoMerge.enabled`, `factory.autoMerge.maxFilesChanged`, and `factory.maintenance.intervalHours`.

## Acceptance criteria

- [ ] Write the failing test `tests/e2e/factory_test.sh`: on the kind cluster, create an epic Work Item, run `nf work decompose NF-1`, assert at least three subtasks exist with dependency ordering, assert only the dependency-free subtasks acquire Agent Runs within 180s, mark the first subtask's run successful, assert its dependent then starts, assert the resulting run carries verdicts from at least two distinct reviewer roles neither of which is the author agent, assert a run with a failing gate is not auto-merged, and finally seed a vulnerable dependency and assert a maintenance Work Item appears with no Agent Run attached. Run `bash tests/e2e/factory_test.sh` — expect FAIL with "Error: unknown command decompose".
- [ ] Add the `nf work decompose`, `nf work subtasks`, and `nf dashboard` commands to the CLI following the structure established in the foundation plan's Task 17.
- [ ] Extend `values.yaml` with the three factory keys, defaulting `autoMerge.enabled` to `false` so a fresh installation never merges without an explicit opt-in.
- [ ] Run `bash tests/e2e/factory_test.sh` — expect PASS.
- [ ] Commit as `feat: add end-to-end software factory test`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
