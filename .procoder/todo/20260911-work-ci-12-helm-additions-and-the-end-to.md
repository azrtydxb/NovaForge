# work-ci 12: Helm additions and the end-to-end work-to-artifact test

Status: open
Created: 2026-09-11

## Description

Plan step 12 of `.procoder/plans/work-ci.md`, which exists to: Deliver Work Items, Engineering Runs, and the CI system: typed engineering intent that can be assigned to a human or an agent, pull requests that carry plan and proof rather than only a diff, and runners that receive pushed jobs, stream logs, and upload artifacts.

This task is done when every file below exists, the interfaces below are available to the
tasks that consume them, each listed test has been run and passes, and the commit named in
the final criterion is on the branch. The criteria are the plan's steps verbatim, in order —
a red-green-commit cycle, so the failing test must be seen failing before the implementation
is written.

Files: `deploy/helm/novaforge/templates/work-reviews.yaml`, `deploy/helm/novaforge/templates/ci-runner.yaml`, `deploy/helm/novaforge/templates/runner.yaml`, `deploy/helm/novaforge/values.yaml`, `tests/e2e/work_ci_test.sh`

Interfaces: produces Services `novaforge-work-reviews:9093` and `novaforge-ci-runner:9094`, and a `runner` Deployment with a configurable replica count and label set from `values.yaml` key `runner.labels`.

## Acceptance criteria

- [ ] Write the failing test `tests/e2e/work_ci_test.sh`: on the kind cluster from the foundation plan, `helm upgrade novaforge ./deploy/helm/novaforge --wait --timeout 10m`, then push a repository containing `.novaforge/workflow.yaml` with a single job running `echo hello && echo hi > out.txt`, poll `nf ci runs` until the run reports `success` within 300s, assert the job log contains `hello`, and assert the artifact `out.txt` is downloadable. Run `bash tests/e2e/work_ci_test.sh` — expect FAIL with "Error: no matching deployment novaforge-ci-runner".
- [ ] Write the three templates, giving the runner Deployment no inbound Service at all, since runners dial out and never accept connections.
- [ ] Extend `values.yaml` with `runner.replicas` defaulting to `2`, `runner.labels` defaulting to `["linux"]`, and MinIO credentials wired into the ci-runner Deployment.
- [ ] Run `bash tests/e2e/work_ci_test.sh` — expect PASS.
- [ ] Commit as `feat: deploy work-reviews, ci-runner, and runners via helm`.

## Evidence

<!-- Filled at close time: the commands run and what their output proved,
     one line per criterion. Empty evidence keeps the task open. -->
